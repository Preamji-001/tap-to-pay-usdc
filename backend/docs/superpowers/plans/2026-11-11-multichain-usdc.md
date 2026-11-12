# Multi-chain USDC: a lesson-by-lesson plan for crypto-pos-base

Date: 2026-11-11. Status: draft for owner review.

**The goal (Level 1).** A customer holds USDC on Ethereum, Base or Polygon. The checkout finds a chain
where they have enough, builds the EIP-3009 authorization for that chain's USDC, and the relayer settles
it on that same chain. The customer never picks a contract.

**Level 2** is paying on Polygon while the merchant receives on Base. It needs a bridge or a solver, and
we only study it at the end (L7).

## The voice-note map (added 2026-11-12)

The owner's description of the product, mapped onto the lessons:

| Voice note | Lesson | State |
|---|---|---|
| Tap the phone on the POS; only a link crosses NFC | N3 | Link handoff works; physical tap untested |
| Detect the customer's wallet | done (EIP-6963, `crypto-pos-base-web`) | Done |
| Choose the chain: customer's preference, else the most convenient | L4 (Option B) + L5 | Backend done (L4: `routes`, binding at authorization); page is L5 |
| Build the EIP-712 / EIP-3009 request for that chain | L2 + L4 | Done (`GET ?chain=`) |
| Customer signs; the backend publishes it, paying gas, on that chain | L3 | Done (fork e2e on Sepolia + Base) |
| Settle into USDC on Arc / Circle Gateway via CCTP | L7 study, then L8 build | To study |

Option B for L4 follows from the voice note: the customer's preference decides the chain at payment
time, not the merchant at creation.

## How to use this plan

There are eight lessons, done in order, and each one ends in a small working change. Every lesson has
four parts:

1. **Learn:** the concept, with what to read and where it appears in our code.
2. **Build:** the smallest change that uses it.
3. **Check yourself:** questions to answer in your own words, in `docs/notes/mc-<lesson>.md`.
4. **Done when:** an acceptance check that can be run.

Finish one lesson, merge it, and pause. Start the next only when the previous one is merged and you can
answer its questions.

## Branches: build on top, one step at a time

```
main
 └─ multichain                  ← base branch, holds the plan; every lesson merges here
     ├─ mc/l1-registry          → PR into multichain
     ├─ mc/l2-per-chain         → PR into multichain (branched after L1 merged)
     └─ …
```

- `multichain` is the base for this goal, in **both** repos (backend and `crypto-pos-base-web`).
- Each lesson branches from the latest `multichain`, and its PR merges back into `multichain`.
- `main` stays untouched until Level 1 works end to end, then `multichain` → `main` in one PR.
- `multichain` was started from `n3-handoff`, so it already contains the tunnel script and the N3 notes.
  Once N3 merges into `main`, rebase or merge `main` into `multichain`.

Starting a lesson:

```
git switch multichain && git pull
git switch -c mc/l1-registry
```

## Feasibility: yes, and it's mostly refactoring

| Already built | Where | Why it carries over |
|---|---|---|
| `chain_id` and `token` on every row | `internal/store/migrations/0001_checkouts.sql:5-6` | The schema already records which chain a checkout uses |
| Domain check at startup | `internal/chain/startup.go:37` `CheckToken` | Run it once per chain |
| EIP-3009 digest, low-s recovery, pre-flight | `internal/chain/` | Chain-independent; they only need the right domain passed in |
| The page signs whatever typed data GET returns | `crypto-pos-base-web/src/lib/typedData.ts` | No per-chain logic needed in the browser |

| Single-chain today | Where |
|---|---|
| One RPC, chain ID, USDC address and domain in config | `internal/config/config.go:23-27`, `:81-84` |
| Chain, token and domain are fixed `Params` | `internal/checkout/service.go:31-41`, `:130` |
| One `chain` and one `relayer` per service | `internal/checkout/service.go:51-59` |
| `CreatedBlock` is taken at create time | `internal/checkout/checkout.go:53` |
| The page hard-codes Sepolia | `useWallet.ts:95`, `App.tsx:70-71` |

**Gate.** L1-L4 are backend-only and can start now. L5 onward needs the phones, and waits until N3 is
closed: the automatic reconnect in `App.tsx:134-145` must be verified on the device and the findings
sheet filled in.

---

## L0. What already exists (reading only, about half a day)

**Learn.** Compare these with our design:

- **x402** (Coinbase): EIP-3009 payments over HTTP, relayed by a "facilitator".
- **Circle Gateway**: one USDC balance usable across chains.
- **CCTP v2**: burn USDC on one chain, mint it on another.
- **Across / Relay / LI.FI**: solver bridges.
- **MetaMask Multichain API**: CAIP-25 sessions that span several chains.
- **Coinbase Commerce / Stripe stablecoin payments**: what we would buy instead of building.

**Build.** A comparison table in `docs/notes/mc-l0-alternatives.md`, covering who relays, which chains,
signature or transaction, and who pays gas.

**Check yourself.**
- What does an x402 facilitator do that our relayer also does?
- Which of these could replace our whole backend?

**Done when.** The table exists, and you can say in one sentence why we're building it ourselves anyway.

## L1. One token, many contracts: the chain registry

**Learn.**
- USDC is a separate contract on each chain. The same EOA works on every EVM chain, but the balances are
  unrelated.
- Accept native Circle USDC only. Polygon's bridged USDC.e is a different token.
- Each USDC has its own EIP-712 name and version. Look them up in Circle's documentation, never from
  memory, and let `CheckToken` confirm them.

| Chain | ID | Gas token | USDC |
|---|---|---|---|
| Ethereum Sepolia | 11155111 | ETH | `0x1c7D…7238` (in use) |
| Base Sepolia | 84532 | ETH | confirm from Circle's docs |
| Polygon Amoy | 80002 | POL | confirm from Circle's docs |

**Build** (branch `mc/l1-registry`; behaviour unchanged):
- Add `internal/chain/registry.go`. Each network has ID, name, RPC, USDC address, decimals (assert 6),
  EIP-712 name and version, confirmations, and an explorer URL.
- Config: `CHAINS=sepolia,base-sepolia,amoy` plus per-chain env vars. With only Sepolia set, everything
  behaves as today.
- Startup runs `CheckToken` for every enabled chain.

**Check yourself.**
- Why is having the same address on two chains not the same as having the same balance?
- What would break if we accepted USDC.e?

**Done when.** All existing tests pass unchanged, and a wrong USDC address on any chain stops startup.

## L2. Why a signature can't cross chains

**Learn.** The EIP-712 domain includes `chainId` and `verifyingContract`, so the digest for a Sepolia
authorization differs from the Base one. Read `internal/chain/` (the digest code) and
`service.go:130-137` (`Domain`, `DomainSeparator`).

**Build** (branch `mc/l2-domain`):
- Make the domain a function of the registry entry instead of fixed `Params`.
- Add a test: sign for Sepolia, verify against the Base domain, and expect a rejection.

**Check yourself.**
- If the domain left out `chainId`, what attack would be possible?
- Why does the nonce alone not prevent that?

**Done when.** The cross-chain replay test passes.

## L3. One relayer, many chains

**Learn.** The relayer keeps the same key and address everywhere, but each chain has its own account
nonce, gas token, funding, send mutex and finality (its confirmation count). Read `internal/chain/relayer.go` and the
reconciler in `internal/checkout/`.

**Build** (branch `mc/l3-per-chain`):
- `Service` holds one `Chain` and one `Relayer` per chain ID.
- The reconciler routes each checkout by its `ChainID`.
- Fund the relayer with Base Sepolia ETH and Amoy POL.

**Check yourself.**
- Why does one mutex for all chains work, but slow things down?
- Why would sharing one nonce counter across chains be a bug?

**Done when.** The anvil-fork e2e test passes on both a Sepolia fork and a Base Sepolia fork, and a Base
checkout settles on Base.

## L4. Late binding: choosing the chain at payment time

**Decision needed first.**
- **Option A:** the merchant picks the chain when creating the checkout. This is simple but not
  automatic.
- **Option B (recommended):** the chain is bound when the customer authorizes.

The rest of this lesson assumes B.

**Learn.**
- MetaMask only exposes the active chain to the page. For three known tokens, `balanceOf` over our own
  RPCs is enough, so no indexer is needed.
- A chain whose RPC is down counts as "unknown", not "zero".

**Build** (branch `mc/l4-late-binding`):
- Migration: `chain_id` and `token` become nullable, with a CHECK that they are both set or both null.
  `created_block` is set when the chain is bound.
- `POST /api/checkouts/{id}/routes {payer}` reads balances on all chains in parallel, with a timeout,
  and caches them for a few seconds.
- `GET /api/checkouts/{id}?chain=84532` returns the typed data for that chain.
- `POST …/authorization` takes `chainId`. The existing pending→submitting gate also binds the chain.
  The server accepts only a chain ID from the registry, never a token address.

**Check yourself.**
- Why bind the chain at authorization and not when `routes` is called?
- What happens if two signatures arrive for two different chains at the same moment?

**Done when.** These tests pass:
- A chain-X signature submitted as chain Y is rejected.
- A chain not in the registry is rejected.
- Of two racing authorizations on two chains, exactly one wins.

## L5. Routing on the page (frontend, `crypto-pos-base-web`)

**Learn.**
- Every chain switch is an extra prompt for the customer.
- Routing policy:
  1. Use the chain MetaMask is already on, if it has enough USDC.
  2. Otherwise, use the eligible chain with the cheapest gas: Base, then Polygon, then Ethereum.
  3. If no chain has enough, show the balances and say so.

**Build** (branch `mc/l5-routing`):
- Generalise `switchChain` (`useWallet.ts:72-87`) to whichever chain was chosen.
- Show "Paying on Base" together with the balances.

**Check yourself.**
- Why prefer the active chain even when another chain is cheaper?

**Done when.** On the desktop extension, all three chains pay end to end, and the switch and add-chain
paths are both covered.

## L6. On the phone (needs N3 closed)

**Learn.** Go back over the N3 lessons. The chain switch inside MetaMask mobile's in-app browser is
untested.

**Build.** Tap, then pay on Base Sepolia and on Amoy from the iPhone. Count every prompt, including the
chain switch and the high-risk alert on each chain.

**Done when.** The N3 findings sheet has a multi-chain section with the real counts.

## L7. Level 2 study (design doc only, no code)

**Learn.** The case: the merchant settles on Base only, and the customer holds USDC on Amoy. Compare
CCTP v2, Circle Gateway and one solver. For each:
- Can the customer sign instead of sending a transaction?
- How long until the merchant's funds are final?
- What does the POS show while the payment is in flight?
- How does a refund work if the second leg fails?

**Done when.** `docs/notes/mc-l7-level2.md` exists. We decide whether to build only after reading it.

## Risks

- MetaMask mobile's chain switch is untested by us. It's the same area as the N3 quirk.
- Public testnet RPCs are rate-limited, so `routes` caches balances.
- Polygon gas spikes, so the relayer sets fee caps per chain.
- The high-risk alert may differ by chain, so record it for each one.

## Rough effort

L0 0.5 d · L1 1-2 d · L2 1 d · L3 2-3 d · L4 3-4 d · L5 2-3 d · L6 1 d · L7 2-3 d (doc).
