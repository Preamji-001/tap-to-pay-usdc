/** "m:ss" for a positive duration, "0:00" once it has passed. */
export function formatRemaining(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`
}

/** 0x3CC8…474c */
export function shortAddress(address: string): string {
  return `${address.slice(0, 6)}…${address.slice(-4)}`
}

export function etherscanTx(hash: string): string {
  return `https://sepolia.etherscan.io/tx/${hash}`
}
