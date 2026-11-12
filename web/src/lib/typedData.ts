import { getAddress, isAddressEqual, isHex, type Address, type Hex } from 'viem'
import type { Checkout, TypedDataField } from './api'

export const SEPOLIA_CHAIN_ID = 11155111

// EIP-3009's message type. The backend's `types` must match it exactly.
export const TRANSFER_WITH_AUTHORIZATION_TYPES = {
  TransferWithAuthorization: [
    { name: 'from', type: 'address' },
    { name: 'to', type: 'address' },
    { name: 'value', type: 'uint256' },
    { name: 'validAfter', type: 'uint256' },
    { name: 'validBefore', type: 'uint256' },
    { name: 'nonce', type: 'bytes32' },
  ],
} as const

/** The backend's typed data disagrees with itself or with what we expect; never sign it. */
export class TypedDataMismatchError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'TypedDataMismatchError'
  }
}

function uint(name: string, value: string | undefined): bigint {
  // Decimal strings only, so nothing is ever rounded through a JS number.
  if (value === undefined || !/^\d+$/.test(value)) throw new TypedDataMismatchError(`message.${name} is not a decimal string`)
  return BigInt(value)
}

function sameFields(a: readonly TypedDataField[] | undefined, b: readonly TypedDataField[]): boolean {
  return a?.length === b.length && a.every((f, i) => f.name === b[i].name && f.type === b[i].type)
}

/**
 * Turns the checkout's `typed_data` plus the signer into viem `signTypedData` arguments.
 * Values are passed through as returned by GET; the checks only refuse data that is
 * inconsistent with the checkout shown on screen. The backend remains the authority.
 */
export function buildSignRequest(checkout: Checkout, from: Address) {
  const { domain, types, primaryType, message } = checkout.typed_data
  const want = TRANSFER_WITH_AUTHORIZATION_TYPES.TransferWithAuthorization

  if (domain.chainId !== SEPOLIA_CHAIN_ID) throw new TypedDataMismatchError(`domain chainId ${domain.chainId} is not Sepolia`)
  if (primaryType !== 'TransferWithAuthorization' || !sameFields(types.TransferWithAuthorization, want)) {
    throw new TypedDataMismatchError('typed data is not an EIP-3009 TransferWithAuthorization')
  }
  if (!isAddressEqual(domain.verifyingContract as Address, checkout.token.address)) {
    throw new TypedDataMismatchError('domain verifyingContract is not the checkout token')
  }
  if (!isAddressEqual(message.to as Address, checkout.merchant)) {
    throw new TypedDataMismatchError('message.to is not the checkout merchant')
  }
  const value = uint('value', message.value)
  if (value !== BigInt(checkout.amount_base_units)) throw new TypedDataMismatchError('message.value is not the checkout amount')
  if (!isHex(message.nonce) || message.nonce.length !== 66) throw new TypedDataMismatchError('message.nonce is not 32 bytes')

  return {
    account: from,
    domain: {
      name: domain.name,
      version: domain.version,
      chainId: domain.chainId,
      verifyingContract: getAddress(domain.verifyingContract),
    },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    primaryType: 'TransferWithAuthorization',
    message: {
      from,
      to: getAddress(message.to),
      value,
      validAfter: uint('validAfter', message.validAfter),
      validBefore: uint('validBefore', message.validBefore),
      nonce: message.nonce as Hex,
    },
  } as const
}
