# crypto-pos-base

Learning POC: an online USDC checkout on Ethereum Sepolia, where the customer signs **once** in
MetaMask and our relayer pays the gas (EIP-3009 `transferWithAuthorization`). No real funds.

This repo is the Go backend. The checkout page is in [`../web`](../web) and the NFC POS app in [`../android-pos`](../android-pos).
Design: `docs/superpowers/specs/2026-10-28-sponsored-checkout-design.md`.

**Status: Stage 3 (relayer + verification).** Checkouts are created, signed, relayed from our account and
verified from their receipts and logs. Stage 4 is the real Sepolia run and the full write-up.

## Run locally

```bash
docker compose up -d --wait            # Postgres 17 on localhost:5433 (dbs: pos, pos_test)
cp .env.example .env                   # then set SEPOLIA_RPC_URL, MERCHANT_API_KEY (openssl rand -hex 32)
                                       # and the relayer keystore + password file (see below)
set -a; . ./.env; set +a
go run ./cmd/server
```

`CHAINS` picks the networks (default `sepolia`; also `base-sepolia`, `amoy`), each with its own
`<NAME>_RPC_URL`. Token facts live in the registry (`internal/chain/registry.go`), not in env. For every
enabled network, startup checks that the RPC is on the right chain, the USDC address has code, the token's
`DOMAIN_SEPARATOR()` matches and it reports 6 decimals. It also logs the relayer's gas balance there. If
any check fails, it refuses to start. A checkout can be paid on any enabled network: its chain is chosen
when the customer authorizes (late binding), and from then on it is verified, relayed and confirmed on that
chain, with one relayer key and a separate account nonce per chain. The first network is the default.

## API

```bash
# Create (merchant only)
curl -s -XPOST localhost:8080/api/checkouts -H "X-API-Key: $MERCHANT_API_KEY" \
  -d '{"amount":"1.25","merchant_reference":"ORDER-1"}'
# → 201 {"id":"co_…","checkout_url":"http://localhost:5173/checkout/co_…","status":"pending",
#        "amount":"1.25","amount_base_units":"1250000","token":{"symbol":"USDC",…},"merchant":"0x3CC8…",…}

# Read (anyone with the id). ?chain=<id> returns the typed data for that chain; without it, the
# bound chain's, or the first network's while no chain is bound. "chain_bound" says which.
curl -s 'localhost:8080/api/checkouts/co_…?chain=84532'

# Where can this payer pay? Their USDC on every enabled chain, and the chain to suggest:
# the preferred one if it holds enough, else the cheapest for the relayer (Base, Polygon, Ethereum).
# Balances are cached for 5 s; a chain that doesn't answer shows "balance": null. Binds nothing.
curl -s -XPOST localhost:8080/api/checkouts/co_…/routes \
  -d '{"payer":"0x…","preferred_chain_id":11155111}'
# → 200 {"amount":"1.25","routes":[{"chain_id":11155111,"network":"Ethereum Sepolia","balance":"34.01",
#        "sufficient":true},…],"recommended_chain_id":11155111}
```

Only `amount` (a decimal string) and `merchant_reference` are accepted. Any other field, such as `merchant` or `token`,
is rejected with `400 invalid_request`. Errors are `{"error": "...", "code": "..."}`.

The checkout JSON includes `typed_data`: the EIP-712 domain, types and message for EIP-3009
`TransferWithAuthorization`. The frontend adds `message.from` and passes it unchanged to `eth_signTypedData_v4`.

```bash
# Authorize (the customer's signature; values are rebuilt from the database, never taken from the body)
curl -s -XPOST localhost:8080/api/checkouts/co_…/authorization \
  -d '{"payer":"0x…","signature":"0x…65 bytes…","chain_id":84532}'
# → 202 checkout JSON with "status":"submitting" and "chain_bound":true
```

`chain_id` is the chain the signature was made for (omit it for the default). The first authorization to
win binds the checkout to its chain; any other, on any chain, gets `409 not_pending`.

| Case | HTTP | `code` |
|---|---|---|
| accepted (incl. nonce already used on chain) | 202 | — |
| bad JSON, unknown fields, bad hex | 400 | `invalid_request` |
| wrong length, v ∉ {27, 28}, high s, signer ≠ payer (incl. signed for another chain) | 400 | `invalid_signature` |
| `chain_id` not enabled on this server | 400 | `unknown_chain` |
| already bound to another chain (also `GET ?chain=`) | 409 | `chain_mismatch` |
| unknown id | 404 | `not_found` |
| not pending | 409 | `not_pending` |
| expired, or within 60 s of expiry | 409 | `expired` |
| balance below amount | 422 | `insufficient_balance` |
| `eth_call` of the transfer reverts | 422 | `simulation_failed` (error = revert reason) |
| RPC error | 503 | `chain_unavailable` (stays pending; retry) |

## Relayer and reconciler

An accepted checkout (`submitting`) is picked up at once by the reconciler, a loop inside the server that also
runs every 3 s over every non-terminal checkout. Checkouts are grouped by chain, and each chain is worked in
parallel by its own lane (its USDC reads and its relayer), so one slow RPC never delays another chain:

| Status | What it does |
|---|---|
| `pending`, past expiry | Looks for the nonce's `AuthorizationUsed` log (someone else may have paid) on its chain, or on **every** chain if none is bound yet, from each chain's head at creation. Found and verified → `confirmed` (and bound to that chain), else `expired`. A chain that can't be searched keeps it `pending` until it can |
| `submitting` | Authorization already used → find that tx from the log and verify it. Otherwise send `transferWithAuthorization` from the relayer → `submitted`. A send that would revert → `failed`; too close to `validBefore` → `failed` |
| `submitted` | Waits for the receipt and `CONFIRMATIONS`. Confirms only if status = 1, the tx went to USDC, and USDC itself logged `AuthorizationUsed(payer, nonce)` and `Transfer(payer, merchant, amount)`. Our tx reverted but the authorization is used → front-run: confirmed with the other tx's hash |

Crash-safe: every step is re-derived from the database and the chain, so a restart resumes where it stopped.
The token's own nonce check makes a second transfer impossible even if we send twice. RPC errors never fail a
checkout; they are retried.

The relayer key is an encrypted keystore:

```bash
cast wallet new ~/.foundry/keystores crypto-pos-relayer   # prompts for a password
printf '%s' '<that password>' > ~/.foundry/crypto-pos-relayer.password && chmod 600 ~/.foundry/crypto-pos-relayer.password
# fund its address with ~0.05 Sepolia ETH; the server logs the address and balance at startup
```

**Limitations (POC):** no fee bumping. A tx the node still knows but never mines stays `submitted` with a warning,
and needs a manual fix. A tx the node forgets for 10 minutes is resent at the same account nonce. There's no rate
limit and no spending cap on the relayer.

Try it without a frontend, using a throwaway key:

```bash
DEVSIGN_KEY=$(openssl rand -hex 32) go run ./cmd/devsign co_… \
  | curl -s -XPOST localhost:8080/api/checkouts/co_…/authorization -d @-
# unfunded key → 422 insufficient_balance; add -tamper to devsign → 400 invalid_signature
```

How it works: [docs/notes/n2a-authorization-concepts.md](docs/notes/n2a-authorization-concepts.md).

Testing on phones (N3, cloudflared tunnel): [docs/notes/n3-handoff.md](docs/notes/n3-handoff.md).

## Tests

```bash
export TEST_DATABASE_URL=postgres://pos:pos@localhost:5433/pos_test?sslmode=disable
go test ./...                                   # Postgres tests skip if TEST_DATABASE_URL is unset
SEPOLIA_RPC_URL=… go test -tags sepolia ./internal/chain/   # live check against Sepolia USDC
go test -tags testnets -run Live ./internal/chain/  # live registry check on every network whose
                                                # <NAME>_RPC_URL is set
go test -tags integration ./internal/e2e/       # whole payment on anvil forks of Sepolia and Base Sepolia (needs anvil,
                                                # SEPOLIA_RPC_URL and TEST_DATABASE_URL). Run it on its own:
                                                # it empties the shared test database, like the store tests
```
