import type { EIP1193Provider } from 'viem'

// EIP-6963: every wallet extension announces its own provider, instead of all of them
// fighting over the single window.ethereum slot (where e.g. Bybit can hide MetaMask).

export type WalletInfo = { uuid: string; name: string; icon: string; rdns: string }
export type WalletDetail = { info: WalletInfo; provider: EIP1193Provider }

export const METAMASK_RDNS = 'io.metamask'

/**
 * Collects wallet announcements. Installed wallets answer the request synchronously;
 * one that finishes loading later announces on its own, and subscribers are told.
 */
export function createWalletStore(target: EventTarget) {
  let wallets: readonly WalletDetail[] = []
  const listeners = new Set<() => void>()

  target.addEventListener('eip6963:announceProvider', (event) => {
    const detail = (event as CustomEvent<WalletDetail | undefined>).detail
    if (!detail?.info?.uuid || !detail.provider) return
    // Wallets re-announce on every request; keep one entry per wallet.
    wallets = [...wallets.filter((w) => w.info.uuid !== detail.info.uuid), detail]
    listeners.forEach((l) => l())
  })
  target.dispatchEvent(new Event('eip6963:requestProvider'))

  return {
    getWallets: () => wallets,
    subscribe(listener: () => void) {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
  }
}

/** MetaMask if it announced itself; otherwise whichever wallet holds window.ethereum. */
export function pickProvider(
  wallets: readonly WalletDetail[],
  legacy: EIP1193Provider | undefined,
): { provider: EIP1193Provider; name: string } | null {
  const metamask = wallets.find((w) => w.info.rdns === METAMASK_RDNS)
  if (metamask) return { provider: metamask.provider, name: metamask.info.name }
  if (!legacy) return null
  return { provider: legacy, name: wallets.find((w) => w.provider === legacy)?.info.name ?? 'Browser wallet' }
}
