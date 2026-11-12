# N2a: how the authorization endpoint works

Read this when you want to understand the backend half of Stage 2. The task plan is
`~/go/crypto-poc/docs/superpowers/plans/2026-11-04-n2a-authorization-endpoint.md`.

## 1. What the customer signs (EIP-3009)

Circle's USDC contract has

```
transferWithAuthorization(from, to, value, validAfter, validBefore, nonce, v, r, s)
```

The customer does **not** send a transaction. They sign a message saying: "move `value` of my USDC to `to`,
valid between `validAfter` and `validBefore`, with this one-time `nonce`". Anyone holding the signature can
submit it on chain and pay the gas. Our relayer (N4) does that, so the customer needs no ETH.

The nonce is random 32 bytes we generate per checkout. The contract records each `(from, nonce)` pair once,
so a signature can never be used twice.

## 2. EIP-712: turning the message into a hash

MetaMask signs *typed data* so it can show readable fields. The hash it signs is

```
digest = keccak256(0x19 0x01 ‖ domainSeparator ‖ structHash)
```

- **domainSeparator** = hash of the token name (`"USDC"`), version (`"2"`), chain id (Sepolia `11155111`)
  and the USDC contract address. It binds the signature to *this* token on *this* chain: the same signature
  is worthless on mainnet or against another contract. Stage 1 already checks at startup that our computed
  value equals the contract's `DOMAIN_SEPARATOR()`.
- **structHash** = `keccak256(typeHash ‖ from ‖ to ‖ value ‖ validAfter ‖ validBefore ‖ nonce)`, where
  `typeHash = keccak256("TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)")`.

Code: `internal/chain/eip3009.go` (`TransferAuthorization.Digest`).

`GET /api/checkouts/{id}` returns a `typed_data` object (domain, types, primaryType, message). The frontend
adds `from` and passes it to MetaMask's `eth_signTypedData_v4` unchanged. A test hashes that JSON with
go-ethereum's independent EIP-712 implementation and checks it equals our `Digest` — that test is the
contract between frontend and backend. Numbers are decimal strings so JavaScript never rounds them.

## 3. Recovering the signer

A signature is 65 bytes: `r` (32) ‖ `s` (32) ‖ `v` (1). From `digest` + signature, `ecrecover` gives back the
signer's address. We never trust numbers from the client: amount, merchant, nonce and expiry are rebuilt from
the database, hashed, and the recovered address must equal the `payer` the client claims. If the customer
signed a different amount (or a different merchant, chain or token), recovery yields some other random
address and we reject with `invalid_signature`.

Strict checks (`RecoverSigner`):
- exactly 65 bytes;
- `v` is 27 or 28 (Ethereum's convention; raw recovery ids 0/1 are rejected);
- **low-s**: `s <= n/2`, where `n` is the secp256k1 curve order. For every valid `(r, s)` there is a second
  valid signature `(r, n − s)`. Allowing only the low half makes each payment have exactly one valid
  signature, which is also what the USDC contract (OpenZeppelin `ECDSA`) requires.

## 4. Pre-flight: ask the chain before saying yes

Read-only calls, free, done before we accept (`internal/chain/usdc.go`):

| Call | Question | If it fails |
|---|---|---|
| `authorizationState(payer, nonce)` | Was this signature already used on chain? | true → accept as `submitting` without relaying; the N4 reconciler finds the transfer |
| `balanceOf(payer)` | Enough USDC? | 422 `insufficient_balance` |
| `eth_call transferWithAuthorization(...)` | Would the real transfer succeed? (runs it without broadcasting) | 422 `simulation_failed` with the revert reason |

If the RPC itself is down → 503 `chain_unavailable`, the checkout stays `pending`, and the customer can retry.

## 5. The idempotency gate

A double-click or two simultaneous requests must not pay twice. The database decides:

```sql
UPDATE checkouts SET status='submitting', payer=$2, signature=$3, updated_at=$4
WHERE id=$1 AND status='pending'
```

Postgres locks the row for the update, so exactly one request changes it (1 row affected); everyone else gets
0 rows → 409 `not_pending`. Only the winner calls the relayer. A test runs 20 goroutines and checks exactly
one wins.

## 6. Order of checks in `Service.Authorize`

1. Load the checkout (bad id or missing → 404).
2. Not `pending` → 409 `not_pending`.
3. `now >= expires_at − 60s` → 409 `expired`. The 60 s margin leaves time for the relayer to land the
   transaction before `validBefore`.
4. Rebuild the digest from the DB, recover, compare to `payer` → 400 `invalid_signature`.
5. `authorizationState` (see table).
6. `balanceOf`.
7. Simulate.
8. Conditional update; winner calls `Relayer.Submit` (a no-op stub until N4).

Cheap local checks run first, so a bad or expired request never touches the RPC.

## 7. Status codes

| Case | HTTP | `code` |
|---|---|---|
| accepted (incl. nonce already used on chain) | 202 | body = checkout JSON |
| bad JSON / unknown fields / bad hex | 400 | `invalid_request` |
| wrong length, bad v, high-s, signer ≠ payer | 400 | `invalid_signature` |
| unknown or malformed id | 404 | `not_found` |
| status not `pending` | 409 | `not_pending` |
| expired or inside the 60 s margin | 409 | `expired` |
| balance < amount | 422 | `insufficient_balance` |
| simulation reverts | 422 | `simulation_failed` |
| RPC error | 503 | `chain_unavailable` |

The request body is exactly `{"payer": "0x…", "signature": "0x…"}`. Sending `amount`, `to` or anything else
is a 400, never silently ignored.

## 8. Trying it without a frontend

`cmd/devsign` signs a checkout's `typed_data` with a throwaway key:

```sh
set -a; . ./.env; set +a
go run ./cmd/server &
ID=$(curl -s -XPOST localhost:8080/api/checkouts -H "X-API-Key: $MERCHANT_API_KEY" -d '{"amount":"1.00"}' | jq -r .id)
DEVSIGN_KEY=$(openssl rand -hex 32) go run ./cmd/devsign $ID \
  | curl -s -XPOST localhost:8080/api/checkouts/$ID/authorization -d @-
# unfunded key → 422 insufficient_balance
```
