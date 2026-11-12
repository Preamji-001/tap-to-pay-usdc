# L4. Late binding: choosing the chain at payment time

Built on 2026-11-12 on branch `mc/l4-late-binding` (stacked on L3). This is the backend half of your voice
note's line *"first I get their preference and build up the request based on that, or by default the most
convenient option"*. The page that uses it is L5.

## 1. The flow now

```
POS creates checkout        → no chain yet; every chain's current block is recorded
page: POST …/routes {payer, preferred_chain_id}
                            → USDC on Sepolia / Base / Amoy, read in parallel; recommended chain
page: GET  …?chain=84532    → typed data with Base's EIP-712 domain
wallet signs (Base domain)
page: POST …/authorization {payer, signature, chain_id: 84532}
                            → signature checked under Base's domain, pre-flight on Base,
                              then pending→submitting AND bind to Base, in one SQL UPDATE
relayer (L3 lane for Base)  → sends on Base, confirms on Base
```

Today's page and the POS app don't send `chain` or `chain_id`. They get the default (the first network
in `CHAINS`), so they keep working unchanged until L5.

## 2. Why bind at authorization (Option B)

| Bind when… | Problem |
|---|---|
| the merchant creates the checkout (Option A) | The POS doesn't know which chain holds the customer's USDC. That's what we're trying to find out. |
| `routes` is called | `routes` only *looks*. The customer may switch accounts or chains, or ask again, before signing. Binding here would lock in a guess. |
| **the authorization wins** | The signature is the customer's actual decision. It names exactly one domain (one chain), and the server proves it by recovering the payer under that domain. |

## 3. One gate, even across chains

Authorizations for two different chains can arrive together, for example a double-tap, or a page that
retries after switching chains. Both signatures are valid, each on its own chain. Only one may be relayed.

The guard is the same atomic `UPDATE` as in N2a, with the binding added to it:

```sql
UPDATE checkouts SET status = 'submitting', chain_id = $5, token = $6, …
WHERE id = $1 AND status = 'pending' AND (chain_id IS NULL OR (chain_id = $5 AND token = $6))
```

Postgres runs row updates one at a time. The first to commit changes `status`, so every other update
matches zero rows and gets `409 not_pending`. `TestMarkSubmitting_RaceAcrossChains_ExactlyOneWins` sends
20 authorizations, half on Sepolia and half on Base, against real Postgres. Exactly one wins, and the row
is bound to the winner's chain.

**Known risk (not solved here).** The losing signature is still a valid authorization on its own chain
until `validBefore`. We never store it or relay it, but if the page leaked it and someone sent it, the
customer would pay twice: once per chain. Our reconciler watches only the bound chain, so it wouldn't
notice. The fix is for the page to sign for **one** chain only (L5). A stronger fix would be to watch the
other chains for the nonce until expiry.

## 4. Where log searches start: `start_blocks`

Before L4, `created_block` (the chain head at creation) told the reconciler where to start looking for
the payment's `AuthorizationUsed` log. Now the chain isn't known at creation, so `Create` reads **every**
chain's head, in parallel with a 5 s timeout each, and stores them all:

```json
start_blocks = {"11155111": 9701234, "84532": 33012345, "80002": 28123456}
```

- On binding, `created_block = start_blocks[chain]`.
- A chain that didn't answer at creation is simply missing. Its searches fall back to a fixed lookback
  (10,000 blocks). `Create` fails only if **no** chain answers.

## 5. Unbound checkouts at expiry

Before, an expired checkout was searched on its one chain. Now nobody may have told us the chain. Example:
the customer signed for Base and someone else sent the signature. So the reconciler:

1. searches **every** chain for the nonce, from that chain's start block;
2. if a verified payment turns up on chain X, marks the checkout `confirmed` **and binds it to X**;
3. if any chain can't be searched (RPC down), keeps it `pending` and tries again. A payment can't be ruled
   out yet;
4. a log that uses our nonce value but doesn't pay *this* checkout is ignored. EIP-3009 nonces are per
   payer, so a stranger can reuse the same 32 bytes. The search keeps going on the other chains;
5. only when every chain answered with nothing does the checkout become `expired`.

## 6. Recommendation policy (`routes`)

1. `preferred_chain_id`, if it holds enough. This is the customer's own choice, or the chain their wallet is
   already on: no "switch network" prompt.
2. Otherwise, of the chains holding enough, the cheapest for **our relayer** to send on. `FeeRank` in the
   registry ranks them by mainnet cost: Base 1, Polygon 2, Ethereum 3. The customer pays no gas either
   way, but we do.
3. Nowhere enough: `recommended_chain_id: null`, and the balances are shown so the page can explain.

A chain whose RPC fails shows `"balance": null`: unknown, which is not the same as `"0"`. Balances are
cached per (chain, payer) for 5 s. Failed reads aren't cached. `routes` is public (it needs only the
unguessable checkout ID), and the cache stops a polling page from hammering the RPCs.

## 7. Database

Migration `0003_late_binding.sql`:
- `chain_id` and `token` are nullable, with `CHECK ((chain_id IS NULL) = (token IS NULL))`.
- `CHECK (chain_id IS NOT NULL OR status IN ('pending','expired'))`: anything sent, confirmed or failed
  happened on one chain.
- `start_blocks jsonb`, backfilled for old rows from `chain_id` and `created_block`. Old rows stay bound
  to their chain and only accept that chain (`409 chain_mismatch` otherwise).

## 8. What proves it

- **Plan done-when:**
  - A chain-X signature submitted as chain Y → `invalid_signature` (`TestAuthorize_CrossChainReplayRejected`).
  - A chain not in the registry → `unknown_chain`, before any RPC call (`TestAuthorize_ChainNotInRegistryRejected`).
  - Racing on two chains → exactly one wins (service test + real-Postgres test).
- Routes policy, unknown vs zero, cache, unbound reconciler paths (`late_binding_test.go`), and HTTP
  shapes (`internal/httpapi/late_binding_test.go`).
- Four deliberate breaks were each caught: dropping the same-chain SQL guard, ignoring the preference,
  expiring while a chain is down, and accepting an unknown chain as the primary.
- **Fork e2e** `TestFork_LateBinding_PaysWhereTheMoneyIs`: the wallet is on Sepolia and the USDC is on Base.
  Routes (real `balanceOf` on both forks) recommends Base. A Sepolia attempt fails pre-flight without
  binding. The Base signature binds and settles on Base.
- **Live** (three testnets): `?chain=` swaps the domain, `?chain=1` gives `unknown_chain`, and `routes`
  answered in 0.27 s.

## Your answers

1. Why bind the chain at authorization and not when `routes` is called?

   >

2. What happens if two signatures arrive for two different chains at the same moment?

   >
