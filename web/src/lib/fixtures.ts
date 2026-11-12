import type { Checkout } from './api'

/** A checkout as the backend returns it. The digest below was computed by the Go backend. */
export const sampleCheckout: Checkout = {
  id: 'co_AAAAAAAAAAAAAAAAAAAAAA',
  checkout_url: 'http://localhost:5173/checkout/co_AAAAAAAAAAAAAAAAAAAAAA',
  status: 'pending',
  amount: '12.5',
  amount_base_units: '12500000',
  token: { symbol: 'USDC', address: '0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238', decimals: 6 },
  merchant: '0x3CC845A0014aFcB0D52F673E0a98039074F1474c',
  chain_id: 11155111,
  merchant_reference: '',
  created_at: '2026-11-08T10:00:00Z',
  expires_at: '2026-11-08T10:15:00Z',
  tx_hash: null,
  typed_data: {
    domain: { name: 'USDC', version: '2', chainId: 11155111, verifyingContract: '0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238' },
    types: {
      TransferWithAuthorization: [
        { name: 'from', type: 'address' },
        { name: 'to', type: 'address' },
        { name: 'value', type: 'uint256' },
        { name: 'validAfter', type: 'uint256' },
        { name: 'validBefore', type: 'uint256' },
        { name: 'nonce', type: 'bytes32' },
      ],
    },
    primaryType: 'TransferWithAuthorization',
    message: {
      to: '0x3CC845A0014aFcB0D52F673E0a98039074F1474c',
      value: '12500000',
      validAfter: '0',
      validBefore: '1793000000',
      nonce: '0x4200000000000000000000000000000000000000000000000000000000000099',
    },
  },
}

export const sampleFrom = '0x00000000000000000000000000000000000000aa' as const

// go-ethereum apitypes.TypedDataAndHash and chain.TransferAuthorization.Digest both give this.
export const sampleDigest = '0xd10b2ccd294e9392ecf38ff9fecf66c974a9dd90864d38c094b453424c442544'
