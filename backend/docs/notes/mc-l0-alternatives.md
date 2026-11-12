# L0. What already exists

The reading part was gathered on 2026-11-11. The **Your table** and **Your answers** sections at the
bottom are yours to fill in.

The lens for the whole lesson: our system is **a customer signs, a relayer pays the gas, and USDC lands
with the merchant.** For each product, ask which of those three steps it replaces.

## 1. x402 (Coinbase): the closest cousin

- **What it is.** HTTP payments that reuse the `402 Payment Required` status code. The server answers
  402 with payment requirements: scheme, network, asset, amount and recipient. The client retries with
  a signed payment attached.
- **The "exact" scheme on EVM chains** is an EIP-3009 `transferWithAuthorization`. That's the same
  signature our checkout asks for.
- **The facilitator is the relayer.** It exposes:
  - `/verify`: checks the signature, amount, recipient and network, without touching the chain.
  - `/settle`: submits the authorization on-chain.
  - `/supported`: lists the schemes and networks it handles.
- The facilitator cannot redirect the funds, because the signature fixes `to` and `value`.
- **Multi-chain** comes from the `network` field in the requirements, in CAIP-2 form such as
  `eip155:84532` for Base Sepolia. Which networks are available depends on the facilitator, so ask its
  `/supported` endpoint.
- **Compare with our code.** `/verify` matches our `POST …/authorization` checks
  (`internal/checkout/service.go`, `Authorize`). `/settle` matches our relayer and reconciler
  (`internal/chain/relayer.go`).

## 2. Circle Gateway: one balance, any chain

- **Deposit.** The user deposits USDC into a Gateway Wallet contract on any supported chain. Circle
  waits for finality on that chain before crediting it.
- **Spend.** To spend on another chain, the user signs an EIP-712 **burn intent** naming the source,
  destination, amount and recipient. The app posts it to Gateway's `/transfer` API and gets back an
  **attestation**.
- **Mint.** Anyone calls the Gateway Minter on the destination chain with that attestation, and USDC
  is minted to the recipient.
- **Speed and trust.** Circle claims under 500 ms on supported routes. Third parties describe a 7-day
  self-withdrawal escape hatch; that is not yet confirmed in Circle's own docs.
- **The catch for us.** The customer must have deposited into Gateway beforehand. Someone with plain
  USDC in MetaMask can't use it without that step.

## 3. Circle CCTP v2: the bridge building block

- **Burn.** On the source chain, call `depositForBurn` and choose Fast or Standard with
  `minFinalityThreshold`. Circle's migration guide gives 1000 for Fast and 2000 for Standard. One
  third-party page says 500, so confirm it in the contracts.
- **Attest, then mint.** Circle's attestation service signs once the burn reaches soft finality (Fast)
  or hard finality (Standard). Anyone then mints on the destination chain.
- **Speed.** Circle says "under 30 seconds on most routes" for Fast, compared with 15+ minutes before.
  Fast Transfer charges a fee and is backed by Circle's "Fast Transfer Allowance" until finality.
- **Hooks** let the mint trigger an extra action on the destination chain.
- **The catch for us.** `depositForBurn` is a **transaction** the USDC holder sends, so the customer
  would need gas on the source chain. That conflicts with "you only sign".

## 4. Solver bridges (Across, Relay, LI.FI)

- **How they work.** A solver pays the recipient on the destination chain from its own funds, and is
  repaid from the user's deposit on the source chain. It's fast because the solver takes the waiting
  risk.
- **Question to research.** Can the deposit be a signature, such as EIP-3009 or Permit2, instead of a
  transaction? This decides whether "you only sign" survives Level 2. Look it up for Across; don't
  assume.

## 5. MetaMask Multichain API and MetaMask Connect

- **The raw API.** `wallet_createSession` opens one CAIP-25 session over several chains, so the page
  could sign for Base without a chain-switch prompt.
- **Status.** MetaMask's docs call it experimental and say it implements an older CAIP-25 version.
  Required scopes are treated as optional, and there are no session ids. One older docs mirror said
  "extension only, not mobile", which may be out of date.
- **MetaMask Connect,** the SDK built on top of it, says it supports mobile through deep links.
- **Relevance.** L5 (routing) has to switch chains. This is the alternative to `wallet_switchEthereumChain`.
  Test it on the iPhone before relying on it, since our in-app browser setup has already surprised us
  once (N3).

## 6. Hosted checkouts (Coinbase Commerce, Stripe stablecoin payments)

What we would buy instead of building: a hosted page, multi-chain USDC, and payouts handled for you.
Look at which chains they accept and whether the customer pays gas. That gives you a UX baseline to
compare against.

---

## Your table (fill this in)

| Product | Who submits the on-chain transaction? | Customer signs or sends a tx? | Who pays gas? | Chains | Replaces which of our 3 steps? |
|---|---|---|---|---|---|
| x402  |coinbase |signs | us | |ERC 
| Circle Gateway |  merchant | sends | them |all  | |
| CCTP v2 | merhcnat | sends | them | CCTP supported | |
| Across (or another solver)  |merchant |sends |us |we decide it | |
| MetaMask Multichain API | n/a | | n/a | | |
| Our system | relayer | signs (EIP-3009) | relayer | Sepolia today | none, it's ours |

### Corrected table (review, 2026-11-11)

| Product | Who submits the on-chain tx? | Customer signs or sends? | Who pays gas? | Chains | Replaces which of our steps? |
|---|---|---|---|---|---|
| x402 | the **facilitator**: Coinbase runs one, but anyone can (we could) | signs (EIP-3009, like ours) | the facilitator | whatever that facilitator's `/supported` lists (e.g. Base, Base Sepolia) | step 2 (relay + gas), and our verify checks |
| Circle Gateway | deposit: customer · mint: anyone (app, merchant or a relayer) | **sends once** (deposit), then only **signs** burn intents | deposit: customer · mint: whoever calls the Minter | Gateway-supported chains only | step 3 across chains (moves the USDC to another chain) |
| CCTP v2 | burn: customer · mint: anyone | **sends** `depositForBurn` on every payment | burn: customer · mint: the caller (+ Fast Transfer fee) | CCTP-supported chains | step 3 across chains |
| Across (solver) | deposit: customer · fill: the **solver** | sends a deposit tx (whether a signature-only deposit exists is the open question) | source: customer · destination: solver (charged in the fee) | Across-supported chains | steps 2 + 3 across chains (the solver pays the merchant) |
| MetaMask Multichain API | none, it sends no transaction | signs, inside one multi-chain session | n/a | the chains in the session's scopes | none of the 3: it improves connect and chain-switch on the page |

Common slip: the **merchant** submits in none of these. The submitter is always a facilitator, a solver,
a relayer or "anyone". Also keep "who sends the tx" separate from "who pays the gas", because a meta-tx
design exists to split exactly those two.

## Your answers

1. What does an x402 facilitator do that our relayer also does? What does ours do that it doesn't?
   Hint: look at the reconciler and front-run handling. - RPC handling nodes listener maintenance , gas fee maintaner
2. Which of these could replace our whole backend? What would we lose by using it? - Node , gas maintenannce
3. Why does CCTP's `depositForBurn` conflict with "you only sign", while Gateway's burn intent doesn't? The Destination Chain Needs Cryptographic Proof
4. In one sentence: why are we building it ourselves anyway? - to learn

### Review (2026-11-11)

1. **Partly right.** RPC nodes, listeners and gas management are real work, but that's work *both* sides
   do. The facilitator just does it for you.
   - **In common:** verify the EIP-712 signature, submit `transferWithAuthorization`, pay the gas.
   - **Only ours:** the checkout state machine with the pending→submitting idempotency gate; the
     reconciler's crash recovery; front-run detection through the `AuthorizationUsed` event; checking the
     `Transfer` after sending; and expiry.
2. **Missed the question.** "Node, gas maintenance" is what we'd *save*, not what replaces us.
   - **Replaces part of the backend:** a hosted x402 facilitator replaces verify and settle.
   - **Replaces all of it:** a hosted checkout (Coinbase Commerce, Stripe).
   - **What we'd lose:** our `co_…` checkout id and the POS polling built on it; control over states and
     recovery; choosing our own chains (we'd be limited to theirs); and we'd pay their fee on every payment. That's a trade-off, not "they're better": hosted wins on speed to launch, ours wins on control, flexibility and learning.
3. **Wrong distinction.** Both need a cryptographic proof (Circle's attestation) before minting on the
   destination chain, so that doesn't separate them. The difference is on the **source** side:
   - `depositForBurn` is a **transaction** the customer must send from their own wallet, paying gas on
     the source chain.
   - Gateway's burn intent is just an **EIP-712 signature**. The transaction (the deposit into the
     Gateway Wallet) happened earlier, once.
4. **Right.** A fuller sentence: to learn, and because the POS-specific parts (checkout ids, the state
   machine, recovery) are ours to control.

## Sources

- x402 facilitator endpoints and checks: [Cronos facilitator API](https://docs.cronos.org/cronos-x402-facilitator/api-reference), [Fireblocks x402 concepts](https://developers.fireblocks.com/docs/x402-concepts), [x402 exact scheme](https://policylayer.com/glossary/x402-exact-payment-scheme), [Avalanche x402 settlement](https://docs.avax.network/academy/blockchain/x402-payment-infrastructure/03-technical-architecture/06-blockchain-settlement)
- Circle Gateway: [unified balance quickstart](https://developers.circle.com/gateway/quickstarts/unified-balance), [transfer how-to](https://developers.circle.com/gateway/howtos/transfer-unified-usdc-balance), [Circle blog](https://www.circle.com/blog/circle-gateway-redefining-crosschain-ux), [Gateway page](https://www.circle.com/gateway), [Eco explainer](https://eco.com/support/en/articles/12068992-circle-gateway-the-future-of-unified-stablecoin-experiences)
- CCTP v2: [Circle CCTP docs](https://developers.circle.com/cctp), [V1 to V2 migration](https://developers.circle.com/cctp/migration-from-v1-to-v2), [Circle blog](https://www.circle.com/blog/cctp-v2-the-future-of-cross-chain), [Across on CCTP v2](https://across.to/blog/cctpv2)
- MetaMask: [About the Multichain API](https://docs.metamask.io/wallet/concepts/multichain-api), [Multichain API reference](https://docs.metamask.io/wallet/reference/multichain-api/), [MetaMask Connect](https://docs.metamask.io/metamask-connect.md)
