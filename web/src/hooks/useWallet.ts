import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import type { Address } from 'viem'
import type { AppConfig } from '../config'
import { makeWalletClient, type WalletClient } from '../lib/clients'
import { createWalletStore, pickProvider, type WalletDetail } from '../lib/eip6963'
import { describeError, isUnknownChainError, type UserError } from '../lib/errors'

// Ask installed wallets to announce themselves once, at load, so the first render already knows them.
const walletStore = typeof window === 'undefined' ? null : createWalletStore(window)
const noWallets: readonly WalletDetail[] = []
const noSubscribe = () => () => {}

export type WalletState = {
  hasProvider: boolean
  walletName: string | null
  walletClient: WalletClient | null
  account: Address | null
  chainId: number | null
  onExpectedChain: boolean
  connecting: boolean
  error: UserError | null
  connect: () => Promise<void>
  switchChain: () => Promise<void>
}

export function useWallet(config: AppConfig): WalletState {
  const wallets = useSyncExternalStore(walletStore?.subscribe ?? noSubscribe, walletStore?.getWallets ?? (() => noWallets))
  const picked = pickProvider(wallets, typeof window === 'undefined' ? undefined : window.ethereum)
  const provider = picked?.provider
  const walletClient = useMemo(() => (provider ? makeWalletClient(config, provider) : null), [config, provider])

  const [account, setAccount] = useState<Address | null>(null)
  const [chainId, setChainId] = useState<number | null>(null)
  const [connecting, setConnecting] = useState(false)
  const [error, setError] = useState<UserError | null>(null)

  // On load: pick up an existing connection silently (eth_accounts never opens a popup),
  // then follow MetaMask as the user switches accounts or networks.
  useEffect(() => {
    if (!provider || !walletClient) return
    // A late-announcing MetaMask can replace the first provider; drop the old one's answers.
    let stale = false
    walletClient.getAddresses().then((a) => !stale && setAccount(a[0] ?? null)).catch(() => {})
    walletClient.getChainId().then((id) => !stale && setChainId(id)).catch(() => {})

    const onAccounts = (accounts: Address[]) => setAccount(accounts[0] ?? null)
    const onChain = (hexId: string) => setChainId(Number(hexId))
    provider.on('accountsChanged', onAccounts)
    provider.on('chainChanged', onChain)
    return () => {
      stale = true
      provider.removeListener('accountsChanged', onAccounts)
      provider.removeListener('chainChanged', onChain)
    }
  }, [provider, walletClient])

  const connect = useCallback(async () => {
    if (!walletClient) return
    setConnecting(true)
    setError(null)
    try {
      const [first] = await walletClient.requestAddresses() // eth_requestAccounts: the "Connect" popup
      setAccount(first ?? null)
      setChainId(await walletClient.getChainId())
    } catch (err) {
      setError(describeError(err))
    } finally {
      setConnecting(false)
    }
  }, [walletClient])

  const switchChain = useCallback(async () => {
    if (!walletClient) return
    setError(null)
    try {
      try {
        await walletClient.switchChain({ id: config.chain.id })
      } catch (err) {
        // MetaMask has never heard of this chain: add it (MetaMask then offers to switch).
        if (!isUnknownChainError(err)) throw err
        await walletClient.addChain({ chain: config.chain })
      }
      setChainId(await walletClient.getChainId())
    } catch (err) {
      setError(describeError(err))
    }
  }, [walletClient, config.chain])

  return {
    hasProvider: Boolean(provider),
    walletName: picked?.name ?? null,
    walletClient,
    account,
    chainId,
    onExpectedChain: chainId === config.chain.id,
    connecting,
    error,
    connect,
    switchChain,
  }
}
