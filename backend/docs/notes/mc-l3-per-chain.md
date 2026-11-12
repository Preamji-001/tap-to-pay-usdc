# L3. One relayer, many chains

Built on 2026-11-12 on branch `mc/l3-per-chain` (stacked on `mc/l2-domain`). Checkouts are still
created on the primary network. What changed is that **every chain you enable can now settle**: each
checkout is checked, relayed and verified on its own chain. L4 then lets the customer pick the chain.

## 1. Same key, different accounts

The relayer has one private key, so it has one address (`0x4E31…a81E`) on every EVM chain. On each chain,
though, that address is a **separate account**:

| Per chain | Why it can't be shared |
|---|---|
| Account nonce | Each chain counts its own transactions. Our relayer's nonce is 12 on Sepolia and 0 on Base. |
| Gas balance | ETH on Sepolia and ETH on Base Sepolia are unrelated balances. Amoy pays in POL. |
| Send lock | The lock exists so two sends don't take the same nonce. Nonces are per chain, so locks are too. |
| Finality | Sepolia and Base wait for 1 confirmation, Amoy for 3 (it reorgs more). |
| Fee market | Each chain has its own base fee, so the fee cap is computed from that chain's head. |

So `main.go` builds one `chain.Relayer` **per network**, all from the same key:

```go
for _, n := range registry.Networks() {
    usdc := chain.NewUSDC(client, n.USDC)
    lanes = append(lanes, checkout.Lane{Network: n, Chain: usdc,
        Sender: chain.NewRelayer(client, relayerKey, n.USDC, n.ChainID)})
}
```

`NewRelayer` signs with `LatestSignerForChainID(chainID)`. The chain ID is inside the signed transaction
(EIP-155), so a relayer transaction for Base can't be replayed on Sepolia. This is the same idea as L2,
one layer down: L2 bound the customer's *signature* to a chain, and EIP-155 binds the relayer's
*transaction* to a chain.

## 2. Lanes: routing by `chain_id`

`checkout.Lane` = network facts + the reads for that chain + the relayer for that chain.

- **Service** holds `Chains` (chain ID → reads). `Authorize` runs its pre-flight (`AuthorizationUsed`,
  `BalanceOf`, the simulated transfer) on **the checkout's chain**. Example: a customer with 100 USDC on
  Sepolia and 0 on Base gets `insufficient_balance` for a Base checkout.
- **Reconciler** keeps one `laneReconciler` per chain. Each pass groups active checkouts by `chain_id`
  and gives each group to its lane. The send/verify logic is unchanged; only *which* chain it talks to
  is new.
- A lane only trusts **its own USDC's logs**. A receipt carrying Base USDC's `Transfer` log proves
  nothing to the Sepolia lane, and the payment is marked failed (`TestReconcile_ReceiptFromAnotherChainsTokenFails`).

## 3. Chains run in parallel

Before, one pass walked every checkout in a single loop. If the Amoy RPC hung for 30 s, Sepolia
confirmations waited 30 s too. Now each chain's group runs in its own goroutine, and the pass ends when
all of them finish.

- A chain that is **down** only delays its own checkouts (`TestReconcile_DownChainDoesNotStopOthers`).
- A chain that **hangs** doesn't hold up the others (`TestReconcile_SlowChainDoesNotDelayOthers`).
  I checked the test by making the loop sequential again, and it failed.
- Within one chain it is still one checkout at a time, the same as before.

## 4. Edge cases decided

| Case | What happens | Why |
|---|---|---|
| A checkout's chain was removed from `CHAINS` while it was active | Skipped with a warning, left as it is | The money may already be moving. Re-enabling the chain resumes it. Expiring it blind could hide a real payment. |
| A row's token isn't its chain's USDC | Not sent, warning every pass | We never create such rows, so this is corruption: stop, don't guess. |
| Chain enabled but no reads wired (a wiring bug) | `Authorize` returns 409 `unsupported_chain` | Same answer as a disabled chain. |
| Two lanes for one chain ID | `NewReconciler` panics at startup | One of them would silently never run. |

## 5. What proves it

- Unit tests (`internal/checkout/lanes_test.go`): routing, separate account nonces (7 vs 300), the same
  tx hash on two chains staying separate, isolation between chains, and the edge cases above. All of
  them pass under `-race`.
- **Fork e2e:** `TestFork_BaseAndSepoliaSettleOnTheirOwnChains` runs two anvil forks at once
  (Sepolia + Base Sepolia) with one reconciler and one relayer key. The payer pays 1.5 USDC on Sepolia
  and 2.25 USDC on Base. Each merchant balance moves by exactly its own amount, and each payment's
  transaction exists only on its own chain.
- **Live startup:** `CHAINS=sepolia,base-sepolia,amoy` starts with `networks=3` and the same relayer
  address on all three. Base Sepolia's gas balance is still 0.0003 ETH, below the 0.01 warning.

Until L4, a Base checkout needs Base as the primary (`CHAINS=base-sepolia,…`). The e2e does this with
`svcOn`.

## Your answers

1. Why does one mutex for all chains work, but slow things down?

   >

2. Why would sharing one nonce counter across chains be a bug?

   >
