# L2. Why a signature can't cross chains

Built on 2026-11-11 on branch `mc/l2-domain`. Checkouts behave the same as before. What changed is
*where the signing domain comes from*: the checkout's own chain, not a fixed server setting.

## 1. What the customer actually signs

MetaMask doesn't sign the message itself. It signs a 32-byte digest:

```
digest = keccak256(0x1901 ‖ domainSeparator ‖ structHash)

domainSeparator = keccak256(EIP712Domain type, "USDC", "2", chainId, verifyingContract)
structHash      = keccak256(TransferWithAuthorization type, from, to, value, validAfter, validBefore, nonce)
```

- `structHash` describes **what** is authorized: who pays whom, how much, when, and with which nonce.
- `domainSeparator` describes **where** it counts: this chain, this token contract.

Change any single field in either one and the digest becomes a different, unrelated number.
`internal/chain/eip3009.go` (`Digest`) and `internal/chain/domain.go` hold this code.

| Network | chainId | verifyingContract | domainSeparator |
|---|---|---|---|
| Sepolia | 11155111 | `0x1c7D…7238` | `0xb90e…20a6` |
| Base Sepolia | 84532 | `0x036C…CF7e` | `0x71f1…9818` |
| Amoy | 80002 | `0x41E9…7582` | `0x5cdd…e9eb` |

## 2. A wrong-chain signature doesn't "fail", it lies

ECDSA recovery takes a digest and a signature and returns *some* public key. If the digest isn't the one
that was signed, recovery still succeeds and returns a random stranger's address.

`TestCrossChainSignatureRecoversStranger` shows it. A Sepolia signature checked under the Base domain
recovers an address, just not the payer's. That's why `Authorize` always does:

```go
signer, err := chain.RecoverSigner(a.Digest(domain), sig)
if signer != payer { return ErrInvalidSignature }
```

The USDC contract does the same comparison on-chain (`signer == from`). So a replayed signature would
revert there too. We just reject it earlier, for free, without an RPC call.

## 3. The change: the domain follows the checkout

**Before:** `Service` held one fixed domain (`Params.ChainID`, `Token`, `EIP712Name`, `EIP712Version`),
and every checkout was verified against it.

**Now:**
- `Params.Networks` is the registry from L1. `Create` stamps the primary network's `chain_id` and
  `token` onto the row, as before.
- `Service.Network(c)` looks up the registry entry for the checkout's **stored** `chain_id`. It refuses
  with `ErrUnsupportedChain` if that chain isn't enabled, or if the stored `token` isn't that chain's USDC.
- `Authorize` and the `typed_data` the page signs both take the domain from `Network(c)`. The page and
  the server therefore always use the same domain for a given checkout.
- HTTP: `ErrUnsupportedChain` becomes **409 `unsupported_chain`** on authorization. A GET of such a row is
  a 500, because it's a server config problem: `CHAINS` was changed while that checkout was pending.

This is groundwork for L4, where one server will hold Sepolia, Base and Amoy checkouts at the same time.
`TestAuthorize_DomainFromRowNotPrimary` already proves a Base row on a Sepolia-primary server verifies
Base signatures only. As a check, I temporarily made the domain come from the primary instead, and that
test failed.

## 4. Tests added

- `TestNetworkDomainSeparator`: each network's computed separator equals the on-chain value.
- `TestCrossChainSignatureRecoversStranger`: different digests on two chains, and recovery returns a
  stranger.
- `TestAuthorize_CrossChainReplayRejected`: Sepolia↔Base and Amoy→Sepolia. Each is rejected before any
  RPC call, the checkout stays `pending`, and the right-chain signature still works afterwards.
- `TestDomainFollowsCheckoutChain` and `TestAuthorize_DomainFromRowNotPrimary`.
- `TestAuthorize_UnsupportedChain` (service) and `TestAuthorization_UnsupportedChain409` (HTTP).

## Your answers

1. If the domain left out `chainId`, what attack would be possible?

   >

2. Why does the nonce alone not prevent that?

   >
