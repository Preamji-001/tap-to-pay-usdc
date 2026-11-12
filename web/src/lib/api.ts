import type { Address, Hex } from 'viem'

export type CheckoutStatus = 'pending' | 'submitting' | 'submitted' | 'confirmed' | 'failed' | 'expired'

export type TypedDataField = { name: string; type: string }

/** EIP-712 data exactly as the backend returns it; `message.from` is added by us. */
export type CheckoutTypedData = {
  domain: { name: string; version: string; chainId: number; verifyingContract: string }
  types: Record<string, TypedDataField[]>
  primaryType: string
  message: Record<string, string>
}

export type Checkout = {
  id: string
  checkout_url: string
  status: CheckoutStatus
  amount: string
  amount_base_units: string
  token: { symbol: string; address: Address; decimals: number }
  merchant: Address
  chain_id: number
  merchant_reference: string
  created_at: string
  expires_at: string
  tx_hash: Hex | null
  typed_data: CheckoutTypedData
}

export function isTerminal(status: CheckoutStatus): boolean {
  return status === 'confirmed' || status === 'failed' || status === 'expired'
}

/** A non-2xx answer from the backend, carrying its `{error, code}` body. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init)
  const body: unknown = await res.json().catch(() => null)
  if (!res.ok) {
    const e = (body ?? {}) as { code?: unknown; error?: unknown }
    throw new ApiError(
      res.status,
      typeof e.code === 'string' ? e.code : 'http_error',
      typeof e.error === 'string' ? e.error : `HTTP ${res.status}`,
    )
  }
  return body as T
}

export function getCheckout(apiBase: string, id: string, signal?: AbortSignal): Promise<Checkout> {
  return request(`${apiBase}/api/checkouts/${encodeURIComponent(id)}`, { signal })
}

/** Sends only the payer and signature: every other value is rebuilt by the backend. */
export function postAuthorization(apiBase: string, id: string, payer: Address, signature: Hex): Promise<Checkout> {
  return request(`${apiBase}/api/checkouts/${encodeURIComponent(id)}/authorization`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ payer, signature }),
  })
}

const ID_PATH = /^\/checkout\/(co_[A-Za-z0-9_-]{22})\/?$/

/** The checkout id from a `/checkout/co_…` path, or null. */
export function checkoutIdFromPath(pathname: string): string | null {
  return ID_PATH.exec(pathname)?.[1] ?? null
}
