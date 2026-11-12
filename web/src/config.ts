import type { Chain } from 'viem'
import { sepolia } from 'viem/chains'

export type AppConfig = {
  apiBaseUrl: string
  chain: Chain
}

export function loadConfig(): AppConfig {
  const apiBaseUrl = import.meta.env.VITE_API_BASE_URL
  if (!apiBaseUrl) throw new Error('VITE_API_BASE_URL is not set. Copy .env.example to .env.local and fill it in.')
  // Payments are Sepolia-only; the backend's typed data is checked against this chain too.
  return { apiBaseUrl: apiBaseUrl.replace(/\/+$/, ''), chain: sepolia }
}
