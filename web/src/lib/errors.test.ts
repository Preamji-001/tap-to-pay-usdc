import { InvalidParamsRpcError, UserRejectedRequestError } from 'viem'
import { describe, expect, it } from 'vitest'
import { ApiError } from './api'
import { describeError, isNotConnectedError, isUnknownChainError } from './errors'
import { TypedDataMismatchError } from './typedData'

describe('describeError', () => {
  it('spots MetaMask mobile refusing to sign for a site it has not really connected', () => {
    const raw = { code: -32602, message: 'Invalid parameters: must provide an Ethereum address.' }
    expect(isNotConnectedError(raw)).toBe(true)
    expect(isNotConnectedError(new InvalidParamsRpcError(Object.assign(new Error(raw.message), raw)))).toBe(true)
    expect(isNotConnectedError({ code: -32602, message: 'Invalid parameters: something else' })).toBe(false)
    expect(describeError(raw).message).toContain('top right')
  })

  it('treats a MetaMask rejection (4001) as a rejection, also when viem wraps it', () => {
    expect(describeError({ code: 4001, message: 'User rejected' }).kind).toBe('rejected')
    expect(describeError(new UserRejectedRequestError(new Error('no'))).kind).toBe('rejected')
  })

  it.each([
    ['not_found', 404, 'does not exist'],
    ['invalid_signature', 400, 'does not match'],
    ['not_pending', 409, 'already paid'],
    ['expired', 409, 'expired'],
    ['insufficient_balance', 422, 'enough USDC'],
    ['chain_unavailable', 503, 'try again'],
    ['invalid_request', 400, 'reload'],
  ])('maps %s to a customer message', (code, status, text) => {
    const e = describeError(new ApiError(status, code, 'server text'))
    expect(e.kind).toBe('error')
    expect(e.message).toContain(text)
  })

  it('shows the revert reason for simulation_failed', () => {
    expect(describeError(new ApiError(422, 'simulation_failed', 'FiatTokenV2: blacklisted')).message).toContain(
      'FiatTokenV2: blacklisted',
    )
  })

  it('names unknown codes instead of hiding them', () => {
    expect(describeError(new ApiError(500, 'internal', 'internal error')).message).toContain('internal')
  })

  it('explains refused typed data and an unreachable server', () => {
    expect(describeError(new TypedDataMismatchError('message.to is not the checkout merchant')).message).toContain('not signed')
    expect(describeError(new TypeError('Failed to fetch')).message).toContain('could not be reached')
  })
})

describe('isUnknownChainError', () => {
  it('detects 4902', () => {
    expect(isUnknownChainError({ code: 4902 })).toBe(true)
    expect(isUnknownChainError({ code: 4001 })).toBe(false)
  })
})
