import { createWalletClient, custom, type EIP1193Provider } from 'viem'
import type { AppConfig } from '../config'

/** Everything that needs the customer goes through their wallet. We only ever ask it to sign. */
export function makeWalletClient(config: AppConfig, provider: EIP1193Provider) {
  return createWalletClient({ chain: config.chain, transport: custom(provider) })
}

export type WalletClient = ReturnType<typeof makeWalletClient>
