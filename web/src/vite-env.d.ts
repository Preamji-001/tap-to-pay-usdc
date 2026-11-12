/// <reference types="vite/client" />
// Adds `window.ethereum?: EIP1193Provider` (MetaMask's injected provider).
import 'viem/window'

declare global {
  interface ImportMetaEnv {
    readonly VITE_API_BASE_URL: string
  }
}
