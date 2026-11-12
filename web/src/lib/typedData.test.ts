import { hashTypedData, recoverTypedDataAddress } from 'viem'
import { generatePrivateKey, privateKeyToAccount } from 'viem/accounts'
import { describe, expect, it } from 'vitest'
import type { Checkout } from './api'
import { sampleCheckout, sampleDigest, sampleFrom } from './fixtures'
import { buildSignRequest, TypedDataMismatchError } from './typedData'

function withChanges(change: (c: Checkout) => void): Checkout {
  const c = structuredClone(sampleCheckout)
  change(c)
  return c
}

describe('buildSignRequest', () => {
  it('hashes to the digest the Go backend verifies', () => {
    expect(hashTypedData(buildSignRequest(sampleCheckout, sampleFrom))).toBe(sampleDigest)
  })

  it('round-trips through a real signature', async () => {
    const account = privateKeyToAccount(generatePrivateKey())
    const req = buildSignRequest(sampleCheckout, account.address)
    const signature = await account.signTypedData(req)
    expect(await recoverTypedDataAddress({ ...req, signature })).toBe(account.address)
  })

  it('keeps uint256 values exact beyond 2^53', () => {
    const big = '1000000000000000000000001'
    const c = withChanges((c) => {
      c.amount_base_units = big
      c.typed_data.message.value = big
    })
    expect(buildSignRequest(c, sampleFrom).message.value).toBe(1000000000000000000000001n)
  })

  it.each<[string, (c: Checkout) => void]>([
    ['another chain', (c) => (c.typed_data.domain.chainId = 1)],
    ['another token', (c) => (c.typed_data.domain.verifyingContract = '0x0000000000000000000000000000000000001234')],
    ['another merchant', (c) => (c.typed_data.message.to = '0x0000000000000000000000000000000000000bad')],
    ['another amount', (c) => (c.typed_data.message.value = '1')],
    ['a non-decimal value', (c) => (c.typed_data.message.validBefore = '1e9')],
    ['a short nonce', (c) => (c.typed_data.message.nonce = '0x42')],
    ['another primary type', (c) => (c.typed_data.primaryType = 'Permit')],
    ['a changed field type', (c) => (c.typed_data.types.TransferWithAuthorization[2].type = 'uint128')],
  ])('refuses %s', (_, change) => {
    expect(() => buildSignRequest(withChanges(change), sampleFrom)).toThrow(TypedDataMismatchError)
  })

  it('accepts addresses in any case', () => {
    const c = withChanges((c) => {
      c.typed_data.message.to = c.typed_data.message.to.toLowerCase()
      c.typed_data.domain.verifyingContract = c.typed_data.domain.verifyingContract.toLowerCase()
    })
    expect(hashTypedData(buildSignRequest(c, sampleFrom))).toBe(sampleDigest)
  })
})
