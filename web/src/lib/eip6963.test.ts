import type { EIP1193Provider } from 'viem'
import { describe, expect, it } from 'vitest'
import { createWalletStore, METAMASK_RDNS, pickProvider, type WalletDetail } from './eip6963'

const fakeProvider = (label: string) => ({ label }) as unknown as EIP1193Provider

function wallet(rdns: string, name: string, uuid = rdns): WalletDetail {
  return { info: { uuid, name, icon: '', rdns }, provider: fakeProvider(name) }
}

const announce = (target: EventTarget, detail: WalletDetail) =>
  target.dispatchEvent(new CustomEvent('eip6963:announceProvider', { detail }))

describe('createWalletStore', () => {
  it('asks wallets to announce themselves', () => {
    const target = new EventTarget()
    let requested = 0
    target.addEventListener('eip6963:requestProvider', () => requested++)
    createWalletStore(target)
    expect(requested).toBe(1)
  })

  it('collects wallets that answer the request, and ones that load later', () => {
    const target = new EventTarget()
    const bybit = wallet('com.bybit', 'Bybit Wallet')
    target.addEventListener('eip6963:requestProvider', () => announce(target, bybit))
    const store = createWalletStore(target)

    let notified = 0
    store.subscribe(() => notified++)
    const metamask = wallet(METAMASK_RDNS, 'MetaMask')
    announce(target, metamask)

    expect(store.getWallets()).toEqual([bybit, metamask])
    expect(notified).toBe(1)
  })

  it('keeps one entry per wallet when it announces twice', () => {
    const target = new EventTarget()
    const store = createWalletStore(target)
    announce(target, wallet(METAMASK_RDNS, 'MetaMask'))
    announce(target, wallet(METAMASK_RDNS, 'MetaMask'))
    expect(store.getWallets()).toHaveLength(1)
  })

  it('ignores malformed announcements', () => {
    const target = new EventTarget()
    const store = createWalletStore(target)
    target.dispatchEvent(new CustomEvent('eip6963:announceProvider', { detail: { info: { name: 'x' } } }))
    expect(store.getWallets()).toEqual([])
  })

  it('returns the same snapshot until something changes', () => {
    const store = createWalletStore(new EventTarget())
    expect(store.getWallets()).toBe(store.getWallets())
  })
})

describe('pickProvider', () => {
  const metamask = wallet(METAMASK_RDNS, 'MetaMask')
  const bybit = wallet('com.bybit', 'Bybit Wallet')

  it('prefers MetaMask even when another wallet took window.ethereum', () => {
    expect(pickProvider([bybit, metamask], bybit.provider)).toEqual({ provider: metamask.provider, name: 'MetaMask' })
  })

  it('falls back to window.ethereum, named by its announcement if it made one', () => {
    expect(pickProvider([bybit], bybit.provider)).toEqual({ provider: bybit.provider, name: 'Bybit Wallet' })
  })

  it('falls back to window.ethereum for wallets that do not support EIP-6963', () => {
    const legacy = fakeProvider('old')
    expect(pickProvider([], legacy)).toEqual({ provider: legacy, name: 'Browser wallet' })
  })

  it('returns null when there is no wallet at all', () => {
    expect(pickProvider([], undefined)).toBeNull()
  })
})
