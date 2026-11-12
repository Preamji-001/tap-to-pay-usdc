import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, checkoutIdFromPath, getCheckout, postAuthorization } from './api'

afterEach(() => vi.unstubAllGlobals())

function stubFetch(status: number, body: unknown) {
  const fn = vi.fn(async () => new Response(JSON.stringify(body), { status }))
  vi.stubGlobal('fetch', fn)
  return fn
}

describe('checkoutIdFromPath', () => {
  it('reads the id from /checkout/co_…', () => {
    expect(checkoutIdFromPath('/checkout/co_AAAAAAAAAAAAAAAAAAAAAA')).toBe('co_AAAAAAAAAAAAAAAAAAAAAA')
    expect(checkoutIdFromPath('/checkout/co_AAAAAAAAAAAAAAAAAAAAAA/')).toBe('co_AAAAAAAAAAAAAAAAAAAAAA')
  })

  it.each(['/', '/checkout/', '/checkout/co_short', '/checkout/co_AAAAAAAAAAAAAAAAAAAAAA/x', '/x/checkout/co_AAAAAAAAAAAAAAAAAAAAAA'])(
    'rejects %s',
    (path) => expect(checkoutIdFromPath(path)).toBeNull(),
  )
})

describe('postAuthorization', () => {
  it('sends only payer and signature', async () => {
    const fn = stubFetch(202, { id: 'co_x', status: 'submitting' })
    await postAuthorization('http://api', 'co_x', '0x00000000000000000000000000000000000000aa', '0x1234')
    const [url, init] = fn.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('http://api/api/checkouts/co_x/authorization')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ payer: '0x00000000000000000000000000000000000000aa', signature: '0x1234' })
  })

  it('throws ApiError with the backend code', async () => {
    stubFetch(422, { error: 'payer balance is below the amount', code: 'insufficient_balance' })
    const err = await postAuthorization('http://api', 'co_x', '0x00000000000000000000000000000000000000aa', '0x').catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 422, code: 'insufficient_balance' })
  })
})

describe('getCheckout', () => {
  it('copes with a non-JSON error body', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('bad gateway', { status: 502 })))
    await expect(getCheckout('http://api', 'co_x')).rejects.toMatchObject({ status: 502, code: 'http_error' })
  })
})
