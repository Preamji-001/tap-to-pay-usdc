import { useCallback, useEffect, useRef, useState } from 'react'
import { getCheckout, isTerminal, type Checkout } from '../lib/api'

const POLL_MS = 3_000

export type CheckoutState = {
  checkout: Checkout | null
  loadError: unknown
  /** Shows a checkout we already have (e.g. the POST response) and keeps polling from there. */
  apply: (c: Checkout) => void
  refresh: () => void
}

/** Loads the checkout and polls it every 3 s until its status is terminal. */
export function useCheckout(apiBase: string, id: string | null): CheckoutState {
  const [checkout, setCheckout] = useState<Checkout | null>(null)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [tick, setTick] = useState(0)
  // Every answer carries the sequence number it was asked with; an older answer
  // arriving after a newer one (or after apply) is dropped.
  const seq = useRef(0)
  const applied = useRef(0)

  const accept = useCallback((n: number, c: Checkout) => {
    if (n < applied.current) return
    applied.current = n
    setCheckout(c)
    setLoadError(null)
  }, [])

  const apply = useCallback((c: Checkout) => accept(++seq.current, c), [accept])
  const refresh = useCallback(() => setTick((t) => t + 1), [])

  useEffect(() => {
    if (!id) return
    const ctrl = new AbortController()
    const n = ++seq.current
    getCheckout(apiBase, id, ctrl.signal)
      .then((c) => accept(n, c))
      .catch((err) => {
        if (!ctrl.signal.aborted) setLoadError(err)
      })
    return () => ctrl.abort()
  }, [apiBase, id, tick, accept])

  const terminal = checkout ? isTerminal(checkout.status) : false
  useEffect(() => {
    if (!id || terminal) return
    const timer = setInterval(refresh, POLL_MS)
    return () => clearInterval(timer)
  }, [id, terminal, refresh])

  return { checkout, loadError, apply, refresh }
}
