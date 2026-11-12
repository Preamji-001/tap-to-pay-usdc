import { BaseError } from 'viem'
import { ApiError } from './api'
import { TypedDataMismatchError } from './typedData'

/** `rejected` means the customer said no in MetaMask: not a failure, just stop. */
export type UserError = { kind: 'rejected' | 'error'; message: string }

// EIP-1193: the user clicked "Reject" in the wallet popup.
const USER_REJECTED = 4001
// MetaMask: wallet_switchEthereumChain for a chain it has never been told about.
const UNRECOGNIZED_CHAIN = 4902

function hasCode(e: unknown, code: number): boolean {
  return typeof e === 'object' && e !== null && 'code' in e && e.code === code
}

// viem wraps provider errors, so the code may sit anywhere in the cause chain.
function hasCodeDeep(err: unknown, code: number): boolean {
  return hasCode(err, code) || (err instanceof BaseError && err.walk((e) => hasCode(e, code)) !== null)
}

// MetaMask mobile: eth_accounts answers, so the page looks connected, yet the site was never really
// connected and every signing method fails with this -32602. Only connecting via MetaMask's UI fixes it.
const INVALID_PARAMS = -32602
const NOT_CONNECTED_TEXT = 'must provide an Ethereum address'

export function isNotConnectedError(err: unknown): boolean {
  const matches = (e: unknown) =>
    hasCode(e, INVALID_PARAMS) && 'message' in (e as object) && String((e as { message: unknown }).message).includes(NOT_CONNECTED_TEXT)
  return matches(err) || (err instanceof BaseError && err.walk(matches) !== null)
}

export function isUnknownChainError(err: unknown): boolean {
  return hasCodeDeep(err, UNRECOGNIZED_CHAIN)
}

// Backend `code`s from POST /api/checkouts/{id}/authorization and GET, in words a customer can act on.
const API_MESSAGES: Record<string, string> = {
  not_found: 'This checkout does not exist. Check the link with the merchant.',
  invalid_signature: 'The signature does not match this checkout. Please try again.',
  not_pending: 'This checkout is already paid or being processed.',
  expired: 'This checkout has expired. Ask the merchant for a new one.',
  insufficient_balance: 'This wallet does not hold enough USDC for this payment.',
  chain_unavailable: 'The payment network could not be reached. Please try again in a moment.',
  invalid_request: 'The payment request was rejected. Please reload the page and try again.',
}

export function describeError(err: unknown): UserError {
  if (hasCodeDeep(err, USER_REJECTED)) return { kind: 'rejected', message: 'You rejected the request in MetaMask. Nothing was paid.' }
  if (isNotConnectedError(err)) {
    return {
      kind: 'error',
      message: 'MetaMask has not connected this page yet. Tap the icon at the top right of the MetaMask browser, connect your account, then tap Pay again.',
    }
  }
  if (err instanceof ApiError) {
    if (err.code === 'simulation_failed') return { kind: 'error', message: `The payment would fail on chain: ${err.message}` }
    return { kind: 'error', message: API_MESSAGES[err.code] ?? `The payment server returned an error (${err.code}).` }
  }
  if (err instanceof TypedDataMismatchError) {
    return { kind: 'error', message: `This checkout's signing data looks wrong, so it was not signed (${err.message}).` }
  }
  // fetch rejects with a TypeError when the server can't be reached at all.
  if (err instanceof TypeError) return { kind: 'error', message: 'The payment server could not be reached.' }
  // viem's shortMessage is generic ("Invalid parameters…"); `details` carries the wallet's own reason.
  if (err instanceof BaseError) {
    const details = err.details && err.details !== err.shortMessage ? ` (${err.details})` : ''
    return { kind: 'error', message: err.shortMessage + details }
  }
  if (err instanceof Error) return { kind: 'error', message: err.message }
  return { kind: 'error', message: String(err) }
}
