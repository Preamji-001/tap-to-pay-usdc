# crypto-pos-base, Milestone 1: sponsored, sign-once USDC checkout

Date: 2026-10-28. Status: draft for owner review.

## Intent and why

The owner is learning smart-contract and dApp development by building. In crypto-poc they built a
merchant-creates / customer approve + `pay` flow, where the customer needed ETH and two MetaMask
transactions. This project removes both pains:

1. **Fixed merchant, payment prepared for the customer.** A trusted backend creates the checkout; the
   customer only reviews it and signs **once**.
2. **Gas sponsorship.** The customer needs **no Sepolia ETH**. Our relayer submits the transaction and
   pays the gas.

It is a testnet learning POC on Ethereum Sepolia with Circle test USDC. No real funds.

Working style (owner's preference, from crypto-poc): explain each concept before writing code, build
one stage at a time, pause for the owner's confirmation after each stage, branch + PR per stage.

## Customer flow

1. The merchant (curl with an API key) calls `POST /api/checkouts` and gets back a checkout URL.
2. The customer opens `https://<frontend>/checkout/<id>`.
3. The page loads the checkout from the backend and shows amount, token and merchant.
4. The customer connects MetaMask (found via EIP-6963) and switches to Sepolia if needed.
5. The customer signs one EIP-712 `TransferWithAuthorization` message.
6. The frontend sends `{payer, signature}` to the backend. Nothing else.
7. The backend verifies the signature against its own stored data, and the relayer submits
   `transferWithAuthorization` to the USDC contract, paying gas.
8. The backend verifies the receipt and events, then marks the checkout `confirmed`. The page polls
   the status and survives refreshes.

## Goals

- Everything in the flow above works end to end on Sepolia with the owner's existing MetaMask
  customer account (`0x84DF…6cD0`) holding USDC.
- The backend is the only source of truth for amount, recipient, token, chain, nonce, expiry and status.
- A checkout can be paid at most once, even under retries, crashes, concurrent requests or someone
  else submitting the signature first.
- Automated tests for every case in "Tests" below, with mocks by default.

## Non-goals (this milestone)

Multi-chain, Fireblocks, production custody, merchant dashboard, refunds, fiat settlement, production
deployment, rate limiting, smart-contract wallets (EIP-1271) as payers, our own Solidity contract,
multiple merchants, multiple relayer keys.

## Anti-goals (would be a failure even if it "works")

- Marking a checkout paid from a tx hash, a client message, or an RPC "accepted" response.
- The frontend being able to change the amount, recipient, token, nonce or validity window.
- The relayer key appearing in the repo, logs, or a plaintext file.
- The relayer being usable as a free gas faucet for arbitrary transactions.

## Verified facts about the token (checked 2026-10-28, read-only)

- Address `0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238`, from Circle's official list
  (developers.circle.com/stablecoins/usdc-contract-addresses, "Ethereum Sepolia").
  Etherscan-verified `FiatTokenProxy` → verified implementation `FiatTokenV2_2`
  `0xDa317C1d3E835dD5F1BE459006471aCAA1289068`, solc 0.6.12.
- EIP-712 domain: `name "USDC"`, `version "2"`, `chainId 11155111`, `verifyingContract` = the proxy.
  Recomputed domain separator matches on-chain `DOMAIN_SEPARATOR()`
  `0xb90e5057db141a932946e64d09ccb7ffc9b00bd79fec26f698d29af0c83320a6`. The contract has no
  `eip712Domain()` (EIP-5267), so the domain must be configured, not discovered.
- `decimals() == 6`. `paused() == false`.
- Present in bytecode: `transferWithAuthorization` (v,r,s form `0xe3ee160e` and bytes form `0xcf092995`),
  `receiveWithAuthorization`, `cancelAuthorization`, `permit`, `authorizationState(address,bytes32)`.
  `TRANSFER_WITH_AUTHORIZATION_TYPEHASH` matches
  `keccak256("TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)")`.
- Source behaviour that matters:
  - Valid only when `validAfter < block.timestamp < validBefore` (strict on both sides).
  - Nonces are per authorizer: `_authorizationStates[from][nonce]`. Used or cancelled → revert
    `"FiatTokenV2: authorization is used or canceled"`.
  - On success it emits `AuthorizationUsed(address indexed authorizer, bytes32 indexed nonce)` and then
    `Transfer(from, to, value)`.
  - `receiveWithAuthorization` requires `msg.sender == to` ("caller must be the payee"), so it cannot be
    used by a third-party relayer paying a merchant EOA.
  - Blacklist and pause checks apply to `from` and `to`.

## Approaches considered

| | Sign once | Customer ETH | Contract | Reference bound on-chain | Front-run risk |
|---|---|---|---|---|---|
| **A. EIP-3009 `transferWithAuthorization` (chosen)** | yes | no | none | via server-chosen nonce in `AuthorizationUsed` | harmless: can only pay the exact amount to the merchant |
| B. EIP-2612 permit + payment contract | yes | no | yes | no (permit doesn't cover a reference) | permit-griefing makes our call revert |
| C. Contract + `receiveWithAuthorization` | yes | no | yes | yes, enforced by the contract | none (`msg.sender == to`) |

Chosen: **A**, by the owner on 2026-10-28. It is the simplest secure option, it is supported by the
verified contract, and needs no Solidity. B was dropped because it adds a contract without binding the
reference. C is a good Milestone 2 if the owner wants the reference enforced on-chain.

What A does *not* do, stated honestly: the token has no "checkout reference" field. The association is:
the **server generates a random 32-byte nonce per checkout** and stores it uniquely. The customer's
signature covers that nonce, and a successful transfer emits `AuthorizationUsed(payer, nonce)`. So an
on-chain event cryptographically proves "the authorization for this checkout's nonce was used", but the
meaning "nonce X = checkout Y" lives only in our database.

## Decisions made with the owner

- Approach A, no smart contract.
- Backend in **Go** (owner knows Go), repo `~/go/crypto-pos-base`.
- Frontend in a separate repo, `~/frontend/crypto-pos-base-web` (exists, `git init` only). React + Vite +
  TypeScript + viem, matching crypto-poc-web, whose EIP-6963 MetaMask discovery code (branch
  `eip6963-wallet-discovery` in `~/frontend/crypto-poc-web`) should be ported.
- Postgres for persistence.
- Checkout creation is an API call protected by a merchant API key; no dashboard.
- Fixed merchant = the owner's MetaMask merchant account `0x3CC845A0014aFcB0D52F673E0a98039074F1474c`.
- GitHub repos are private under `Preamji-001`, created when the first stage is pushed.

## Design (my calls are marked *[call]*)

### Backend shape

One Go binary (`cmd/server`): HTTP API + a background reconciler goroutine, sharing one Postgres pool
and one chain client. Stdlib `net/http` routing, `pgx/v5`, `go-ethereum` (`ethclient`, `accounts/keystore`,
`signer/core/apitypes` or hand-built EIP-712 hashing). *[call]* Package layout is left to the builder,
but chain access must sit behind an interface so tests can mock it.

Config via environment variables, with `.env.example` committed and `.env` git-ignored:
`DATABASE_URL`, `SEPOLIA_RPC_URL`, `CHAIN_ID=11155111`, `USDC_ADDRESS`, `USDC_EIP712_NAME=USDC`,
`USDC_EIP712_VERSION=2`, `MERCHANT_ADDRESS`, `MERCHANT_API_KEY`, `RELAYER_KEYSTORE_PATH`,
`RELAYER_PASSWORD_FILE`, `FRONTEND_BASE_URL`, `CORS_ORIGIN`, `CHECKOUT_TTL` (default 15m),
`MAX_AMOUNT_BASE_UNITS` (default 1000 USDC = 1000000000), `CONFIRMATIONS` (default 1).

Startup checks (fail fast): RPC chain id equals `CHAIN_ID`; the configured USDC address has code;
the computed domain separator equals on-chain `DOMAIN_SEPARATOR()`; the relayer keystore decrypts;
log the relayer address and its ETH balance (warn below 0.01 ETH). This turns a wrong env var into a
startup error instead of a bad payment.

### Relayer key *[call]*

A new dedicated account, created as an encrypted keystore with `cast wallet new` (same practice as
crypto-poc's `sepolia-deployer`). The backend reads the keystore file plus a password file whose
path is in env. Neither file is in the repo. Fund it with about 0.05 Sepolia ETH from the deployer.
Never log the key, the password, or full signatures.

### Data model *[call on exact columns]*

One table `checkouts`, with SQL migrations embedded in the binary and applied at startup:

- `id` text PK: `co_` + 22-char base64url of 16 random bytes (opaque, unguessable).
- `amount_base_units` numeric(78,0) > 0; `merchant` / `token` (20-byte hex, lowercased); `chain_id`.
- `nonce` bytea(32), **unique**, random per checkout.
- `valid_after` = 0, `valid_before` = `expires_at` as a unix timestamp; `created_at`, `expires_at`.
- `merchant_reference` text, optional, the merchant's own order id (stored and echoed, never signed).
- `status` (see below), `payer`, `signature`, `tx_hash`, `failure_reason`, `confirmed_block`, `updated_at`.

### States

```
pending ──(valid signature accepted)──▶ submitting ──(tx sent, hash stored)──▶ submitted ──▶ confirmed
   │                                        │                                      └──▶ failed
   └──(now ≥ expires_at)──▶ expired          └──(crash) reconciler resumes
```

- `pending`: waiting for the customer's signature.
- `submitting`: signature accepted and stored; the relayer owns it. Exists so a crash between "accepted"
  and "tx hash saved" is recoverable.
- `submitted`: tx hash stored, waiting for the receipt.
- `confirmed`: receipt and events verified (terminal).
- `failed`: a mined revert, or verification mismatch (terminal, with `failure_reason`). *[call]* The
  merchant creates a new checkout; no retry inside the same checkout this milestone.
- `expired`: no valid signature before `expires_at` (terminal). Computed on read and also written by
  the reconciler.

### API

- `POST /api/checkouts` (header `X-API-Key`). Body `{"amount": "12.50", "merchant_reference": "ORDER-1"}`.
  The amount is a decimal string, parsed with exact decimal math (max 6 decimals, > 0, ≤ max), never a
  float. Returns `201 {id, checkout_url, ...checkout}`. The recipient, token and chain come only from config.
- `GET /api/checkouts/{id}`: the authoritative state, plus the exact EIP-712 `domain`, `types`,
  `primaryType` and `message` fields (everything except `from`) the frontend must sign. Unknown id → 404.
- `POST /api/checkouts/{id}/authorization`. Body `{"payer": "0x…", "signature": "0x…(65 bytes)"}`.
  It accepts nothing else: amount, recipient, nonce and the validity window are rebuilt from the DB.
  Responses: `202` with the new state; `409` if not `pending` (already submitting / paid / failed) or
  expired; `400` for a malformed or wrong signature; `422` when the simulation fails (e.g. insufficient
  USDC balance), leaving the status `pending`.

### Authorization handling (the core)

1. Load the checkout. Reject if not `pending` or `now ≥ expires_at - safety margin` (*[call]* 60 s, so a
   tx doesn't arrive after `validBefore`).
2. Rebuild the typed-data digest from DB fields + `from = payer`. Signature must be 65 bytes, low-s,
   v ∈ {27, 28}; ecrecover must equal `payer`.
3. Pre-flight on chain. *[call]* If `authorizationState(payer, nonce)` is already true, the payment may
   already have happened elsewhere: store payer + signature, set `submitting` (via the step-4 gate), and
   let the reconciler find the tx through the logs. Return 202. Otherwise check `balanceOf(payer) ≥ amount`
   and that an `eth_call` of the exact `transferWithAuthorization` succeeds. If either fails, return 422
   and stay `pending`.
4. Conditional update `UPDATE … SET status='submitting', payer, signature WHERE id=$1 AND status='pending'`.
   Exactly one concurrent request wins; the others get 409. This is the idempotency gate.
5. Relayer sends `transferWithAuthorization(from, to, value, validAfter, validBefore, nonce, v, r, s)`
   from the relayer key. Gas is estimated by the node + 20% *[call]*, EIP-1559 fees come from the node,
   and one mutex serializes relayer sends so its account nonces don't collide. Store `tx_hash`, set
   `submitted`.

Why double payment is impossible even if our code retries: the token itself refuses a second use of the
same `(payer, nonce)`. Our states prevent wasted gas; the token guarantees at most one transfer.

### Verification and reconciler

A goroutine runs every few seconds *[call: 3 s]* over non-terminal rows:

- `submitted`: fetch the receipt. Missing → keep waiting (an RPC error is not a failure). When it's mined
  with ≥ `CONFIRMATIONS`:
  confirm only if `status == 1`, `to == USDC`, and the logs emitted by the USDC address contain both
  `AuthorizationUsed(payer, nonce)` and `Transfer(payer, merchant, amount)` with exact values. A reverted
  receipt or a mismatch → `failed` with the reason.
- **Someone else submitted first** (our tx reverted with "used or canceled", or our send was rejected):
  query `AuthorizationUsed` logs filtered by both indexed topics (`payer`, `nonce`) on the USDC
  address, then verify *that* tx's receipt. *[call]* For a tx we didn't send, skip the `to == USDC`
  check (a third party may call through a multicall or relayer contract) and verify only `status == 1`
  plus the two logs **emitted by the USDC address**. If it matches → `confirmed` (`tx_hash` set to the
  real one). The customer paid; someone else paid the gas.
- `submitting` (we crashed before saving a hash): if `authorizationState(payer, nonce)` is true, find the
  tx via the log query above; otherwise, if still inside the validity window, resubmit the stored
  signature (safe because of the token's nonce check); otherwise `failed`.
- `pending` past `expires_at`: *[call]* before expiring, query `AuthorizationUsed` logs filtered by the
  `nonce` topic alone (the payer isn't known yet) since the checkout's creation block. If found and its
  receipt verifies (with `from` taken from the log) → `confirmed`, otherwise `expired`.
- Stuck or dropped relayer tx *[call]*: store the relayer's account nonce (`relayer_nonce` column) with
  each send. **No automatic fee bumping this milestone.** Resubmit only when the node no longer knows the
  tx (`TransactionByHash` not found after 10 minutes) **and** `authorizationState` is still false, reusing
  the stored account nonce so nothing queues behind it. A tx that is known but stuck stays `submitted`
  and is logged as a warning. Fixing it is manual, and the README documents this as a POC limitation.

### Relayer abuse limits

Only checkouts created with the merchant API key can be relayed. Each one is relayed at most once, the
relayer only ever calls `transferWithAuthorization` on the configured USDC address, and the
signature plus simulation are checked before spending gas. Rate limiting and a relayer spending cap are out of scope (noted in the README).

### Frontend (`~/frontend/crypto-pos-base-web`)

- One page, `/checkout/:id` (*[call]* parse `location.pathname`, no router library). Config
  `VITE_API_BASE_URL`.
- It shows amount (formatted from base units, 6 decimals), "USDC", merchant address, expiry countdown and status.
- Connect via EIP-6963 (MetaMask by rdns `io.metamask`); require chain `11155111`; offer
  `wallet_switchEthereumChain`.
- Sign with viem `signTypedData` using the domain, types and message **exactly as returned by GET**,
  plus `from` = the connected account. Then POST `{payer, signature}`.
- After submit, and on every page load, poll `GET` every 3 s until the status is terminal.
- Clear messages for a rejected signature (EIP-1193 code 4001), wrong network, no MetaMask, expired,
  already paid, insufficient USDC (422), and backend errors.
- Never asks for a private key or seed, and never sends a transaction from the customer's wallet.

### Local development *[call]*

`docker compose` for Postgres 17. Live-like testing uses **`anvil --fork-url $SEPOLIA_RPC_URL`**: a local
fork contains the real FiatTokenV2_2 bytecode and the owner's real balances, so the whole flow can run
locally with a funded anvil relayer key and no Sepolia gas. The real Sepolia run is the final manual step.

## Tests

Go unit tests use a mocked chain interface and a real Postgres (docker compose, `TEST_DATABASE_URL`,
skipped with a clear message if unset). Required cases:

- amount or recipient tampering (sig over different values → 400; client can't send them anyway)
- wrong chain id / wrong token address in the signed domain → 400
- malformed signature, wrong signer, high-s signature → 400
- expired checkout / inside safety margin → 409
- authorization nonce already used on chain → 202, then the reconciler confirms via logs (or fails on mismatch)
- `pending` checkout paid by an outside relayer → confirmed instead of expired
- duplicate submission (sequential) → second gets 409
- concurrent submissions (e.g. 20 goroutines) → exactly one relayer send
- reverted receipt → `failed`
- status 1 receipt but wrong `Transfer` (amount, to, token address, missing `AuthorizationUsed`) → `failed`
- RPC errors and delayed receipts → stays `submitted`, then confirms later
- crash recovery from `submitting`
- front-run by a third party → `confirmed` with the third party's tx hash

Plus an anvil-fork integration test (build tag `integration`) that runs the real contract end to end, and
a frontend vitest for typed-data construction and error mapping. Tests that didn't run are reported as not run.

## Stages (one branch + PR each, pause after each)

1. **Backend skeleton:** Go module, config, startup checks, Postgres + migrations, create and GET
   endpoints, tests. (Concepts: base units, opaque ids, why the server is the source of truth.)
2. **Signing:** frontend page + MetaMask + EIP-712 signing; backend authorization endpoint with signature
   verification and the idempotency gate, with relaying stubbed. (Concepts: EIP-712, ecrecover, nonces.)
3. **Relayer + verification:** relayer key, sending, reconciler, receipt and event checks, front-run and crash
   paths, anvil-fork test. (Concepts: who pays gas, receipts vs hashes, logs and topics.)
4. **Sepolia run + docs:** fund the relayer, a real payment, README (architecture, env, setup, API examples,
   test results, limitations).

## Left to the builder

Package layout, migration tool choice (or hand-rolled embed), exact log format, HTTP error body shape
(keep consistent JSON `{error, code}`), frontend styling, and test helper structure.
