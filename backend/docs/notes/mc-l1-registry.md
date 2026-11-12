# L1. One token, many contracts: the chain registry

Built on 2026-11-11 on branch `mc/l1-registry`. Checkouts behave exactly as before: the server now
**knows about** three chains and checks all of them, but it still settles only on the first one. The
**Your answers** section at the bottom is yours to fill in.

## 1. "USDC" is three unrelated contracts

| Network | Chain ID | Gas | USDC (Circle, native) | Explorer |
|---|---|---|---|---|
| Ethereum Sepolia | 11155111 | ETH | `0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238` | sepolia.etherscan.io |
| Base Sepolia | 84532 | ETH | `0x036CbD53842c5426634e7929541eC2318f3dCF7e` | base-sepolia.blockscout.com |
| Polygon Amoy | 80002 | POL | `0x41E94Eb019C0762f9Bfcf9Fb1E58725BfB0e7582` | amoy.polygonscan.com |

- The addresses come from Circle's list (developers.circle.com/stablecoins/usdc-contract-addresses).
- The name, version and decimals were read on-chain with `cast call`. All three report `"USDC"`, `"2"`
  and `6`.
- Each chain is a separate ledger. Your EOA (one private key gives one address) exists on all of them,
  but its USDC balance on Base is stored in Base's USDC contract, and Sepolia's contract knows nothing
  about it.
- **Seen live in this lesson.** Base Sepolia's USDC address *also has code on Sepolia*, but it isn't USDC
  there: calling `DOMAIN_SEPARATOR()` on it reverted. The same address on another chain can be a
  completely different contract. That's why the registry ties every address to one chain ID.

## 2. Native USDC only (no USDC.e)

- Bridged tokens such as Polygon's old `USDC.e` are wrapped IOUs from a bridge contract. They are not
  Circle's FiatToken.
- They may not implement EIP-3009 `transferWithAuthorization` at all, or they may use a different EIP-712
  name and version.
- They also carry the bridge's risk, and Circle won't redeem them.
- The registry lists exactly one token per chain, the native one, so there is nothing to choose wrongly.

## 3. Facts in code, deployment settings in env

| Lives in `internal/chain/registry.go` | Lives in `.env` |
|---|---|
| chain ID, USDC address, decimals, EIP-712 name/version, gas token, explorer, default confirmations | `CHAINS`, `<NAME>_RPC_URL`, optional `<NAME>_CONFIRMATIONS` |

- Public facts about a token never change per deployment, so a typo in `.env` shouldn't be able to point
  us at the wrong contract.
- The old single-chain variables (`CHAIN_ID`, `USDC_ADDRESS`, `USDC_EIP712_*`) are still accepted, but
  only as a cross-check: if one disagrees with the registry, startup fails.

## 4. Trust, but verify at startup

`CheckNetwork` runs for every enabled chain. It covers `CheckToken` plus one new check:

1. The RPC's chain ID matches the registry. A Base URL pasted into `AMOY_RPC_URL` is caught here.
2. The USDC address has code.
3. The token's `DOMAIN_SEPARATOR()` equals the one we compute from (name, version, chainId, address).
4. **New:** `decimals()` returns 6. Our amounts are 6-decimal base units, so an 18-decimal token would
   move a wildly wrong amount.

The offline unit tests also pin each chain's on-chain domain separator. So if the registry drifts, the
tests fail even without network access.

## 5. Confirmations differ per chain

- Sepolia and Base Sepolia default to 1. Amoy defaults to 3, because Polygon PoS has had short reorgs.
- These defaults are a policy choice, not a fact from documentation. Change them per chain with
  `<NAME>_CONFIRMATIONS`.
- L3 revisits finality per chain.

## What changed

- `internal/chain/registry.go`: `Network`, the built-in table, `Registry` (`Primary`, `ByID`) and
  `CheckNetwork`.
- `internal/config`: `CHAINS` and per-chain env vars. `Config.Networks` is the list, and the old flat
  fields mirror the primary.
- `cmd/server/main.go`: dials and checks every network, and logs the relayer's gas balance on each. The
  service and reconciler still use the primary.
- Tests:
  - Registry facts, on-chain domain fixtures, `NewRegistry` validation, `CheckNetwork`, and the
    multi-chain config.
  - `-tags testnets` runs the same checks against the live chains, including a wrong address on each
    one.

Try it:

```bash
set -a; . ./.env; set +a
CHAINS=sepolia,base-sepolia,amoy BASE_SEPOLIA_RPC_URL=https://sepolia.base.org \
  AMOY_RPC_URL=https://polygon-amoy-bor-rpc.publicnode.com go run ./cmd/server
# then try a wrong one, e.g. AMOY_RPC_URL=https://sepolia.base.org → "chain id mismatch"
```

The relayer `0x4E31…a81E` holds 0 ETH on Base Sepolia and 0 POL on Amoy, so startup warns about both.
Fund them before L3.

## Your answers

1. Why is having the same address on two chains not the same as having the same balance?

   > Two different token address 

2. What would break if we accepted USDC.e?

   >No exchange will accept the broken contract

## Review

1. **Half right.** The addresses do differ, but that isn't the reason. Each chain is a separate ledger, and
   a balance is just a number stored in *that chain's* token contract. Nothing copies state between
   chains. Even if Circle deployed USDC at the identical address everywhere (some tokens do, using
   CREATE2), your Base balance and your Sepolia balance would still be unrelated. The address only tells
   you *which contract to ask*, and the chain ID tells you *which ledger* you're asking.
   - A fuller sentence: the same EOA exists on every chain, but each chain keeps its own state, so its
     balance has to be read on each chain separately.

2. **Not quite.** USDC.e isn't broken, and exchanges do list it. What breaks is **our** flow:
   - **Signing:** it's a different contract, made by a bridge. It may not implement EIP-3009
     `transferWithAuthorization`. Even if it has a signing scheme, its EIP-712 name and version differ, so
     our signature would never verify. The relayer would get reverts, or we'd need separate code.
   - **Value:** it's an IOU backed by USDC locked in a bridge. If the bridge is hacked or paused, the
     merchant holds something worth less than $1. Circle doesn't redeem it directly.
   - **Accounting:** "USDC" would quietly mean two assets on one chain. A payer could pay with the
     cheaper one, and the merchant's books wouldn't match.
   - A fuller sentence: accepting USDC.e means accepting a different token with a different signature
     domain and a bridge's risk, so the signature fails or the merchant takes on value they didn't agree
     to.
