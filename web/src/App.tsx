import { useMemo, useState } from 'react'
import type { Address } from 'viem'
import { loadConfig, type AppConfig } from './config'
import { useCheckout } from './hooks/useCheckout'
import { useNow } from './hooks/useNow'
import { useWallet } from './hooks/useWallet'
import { ApiError, checkoutIdFromPath, postAuthorization, type Checkout } from './lib/api'
import { describeError, isNotConnectedError, type UserError } from './lib/errors'
import { etherscanTx, formatRemaining, shortAddress } from './lib/format'
import { buildSignRequest } from './lib/typedData'

// The backend refuses a signature within 60 s of expiry, so the page stops offering one too.
const SAFETY_MARGIN_MS = 60_000

export default function App() {
  const config = useMemo(() => {
    try {
      return loadConfig()
    } catch (err) {
      return err as Error
    }
  }, [])
  const id = checkoutIdFromPath(window.location.pathname)

  if (config instanceof Error) return <Shell><p className="notice error">{config.message}</p></Shell>
  if (!id) return <Shell><p className="notice">Open the checkout link you got from the merchant.</p></Shell>
  return <CheckoutPage config={config} id={id} />
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <main className="shell">
      <div className="card">{children}</div>
      <p className="footnote">You only sign. The network fee is paid for you, and no ETH is needed.</p>
    </main>
  )
}

function CheckoutPage({ config, id }: { config: AppConfig; id: string }) {
  const { checkout, loadError, apply, refresh } = useCheckout(config.apiBaseUrl, id)

  if (!checkout) {
    if (loadError) return <Shell><p className="notice error">{describeError(loadError).message}</p></Shell>
    return <Shell><p className="notice">Loading checkout…</p></Shell>
  }
  return (
    <Shell>
      <Summary checkout={checkout} />
      <StatusPanel checkout={checkout} config={config} apply={apply} refresh={refresh} />
    </Shell>
  )
}

function Summary({ checkout }: { checkout: Checkout }) {
  return (
    <header className="summary">
      <p className="label">Amount due</p>
      <p className="amount">
        {checkout.amount} <span className="symbol">{checkout.token.symbol}</span>
      </p>
      <dl className="details">
        <dt>To</dt>
        <dd className="mono" title={checkout.merchant}>{shortAddress(checkout.merchant)}</dd>
        {checkout.merchant_reference && (
          <>
            <dt>Reference</dt>
            <dd>{checkout.merchant_reference}</dd>
          </>
        )}
        <dt>Network</dt>
        <dd>Sepolia testnet</dd>
      </dl>
    </header>
  )
}

type Props = { checkout: Checkout; config: AppConfig; apply: (c: Checkout) => void; refresh: () => void }

function StatusPanel(props: Props) {
  const { checkout } = props
  switch (checkout.status) {
    case 'pending':
      return <PayPanel {...props} />
    case 'submitting':
    case 'submitted':
      return (
        <section className="status processing">
          <p className="status-title">Processing payment…</p>
          <p>Your signature was accepted. The payment is being sent on chain; this page updates by itself.</p>
          <TxLink checkout={checkout} />
        </section>
      )
    case 'confirmed':
      return (
        <section className="status success">
          <p className="status-title">Payment confirmed</p>
          <TxLink checkout={checkout} />
        </section>
      )
    case 'failed':
      return (
        <section className="status error">
          <p className="status-title">Payment failed</p>
          <p>The payment did not go through. Ask the merchant for a new checkout.</p>
          <TxLink checkout={checkout} />
        </section>
      )
    case 'expired':
      return (
        <section className="status muted">
          <p className="status-title">Checkout expired</p>
          <p>Ask the merchant for a new one.</p>
        </section>
      )
  }
}

function TxLink({ checkout }: { checkout: Checkout }) {
  if (!checkout.tx_hash) return null
  return (
    <a className="tx" href={etherscanTx(checkout.tx_hash)} target="_blank" rel="noreferrer">
      View transaction {shortAddress(checkout.tx_hash)}
    </a>
  )
}

type Wallet = NonNullable<ReturnType<typeof useWallet>['walletClient']>

/**
 * Sign, recovering once from MetaMask mobile's "looks connected but isn't" state (see
 * isNotConnectedError): drop this site's account permission and ask again, which opens a real
 * Connect screen. If that still fails, the caller shows how to connect from MetaMask's own menu.
 */
async function signAuthorization(client: Wallet, checkout: Checkout, account: Address) {
  try {
    return { payer: account, signature: await client.signTypedData(buildSignRequest(checkout, account)) }
  } catch (err) {
    if (!isNotConnectedError(err)) throw err
    await client.request({ method: 'wallet_revokePermissions', params: [{ eth_accounts: {} }] } as never).catch(() => {})
    await client.requestPermissions({ eth_accounts: {} })
    const [payer] = await client.getAddresses()
    if (!payer) throw err
    return { payer, signature: await client.signTypedData(buildSignRequest(checkout, payer)) }
  }
}

function PayPanel({ checkout, config, apply, refresh }: Props) {
  const wallet = useWallet(config)
  const now = useNow()
  const [step, setStep] = useState<'sign' | 'submit' | null>(null)
  const [error, setError] = useState<UserError | null>(null)

  const remaining = Date.parse(checkout.expires_at) - now
  const tooLate = remaining <= SAFETY_MARGIN_MS

  async function pay() {
    if (!wallet.walletClient || !wallet.account) return
    setError(null)
    setStep('sign')
    try {
      const { payer, signature } = await signAuthorization(wallet.walletClient, checkout, wallet.account)
      setStep('submit')
      apply(await postAuthorization(config.apiBaseUrl, checkout.id, payer, signature))
    } catch (err) {
      setError(describeError(err))
      // The checkout moved on without us (paid elsewhere, or expired): show its real state.
      if (err instanceof ApiError && (err.code === 'not_pending' || err.code === 'expired')) refresh()
    } finally {
      setStep(null)
    }
  }

  const shownError = error ?? wallet.error
  return (
    <section className="pay">
      <p className={`countdown ${tooLate ? 'late' : ''}`}>
        {tooLate ? 'Too close to expiry to pay' : `Expires in ${formatRemaining(remaining)}`}
      </p>

      {!wallet.hasProvider ? (
        <p className="notice">
          No wallet found. Open this page in the MetaMask app, or install the MetaMask browser extension.
        </p>
      ) : !wallet.account ? (
        <button onClick={wallet.connect} disabled={wallet.connecting || tooLate}>
          {wallet.connecting ? 'Check MetaMask…' : `Connect ${wallet.walletName ?? 'wallet'}`}
        </button>
      ) : !wallet.onExpectedChain ? (
        <>
          <p className="notice">MetaMask is on another network. This payment is on Sepolia.</p>
          <button onClick={wallet.switchChain}>Switch to Sepolia</button>
        </>
      ) : (
        <>
          <p className="account">Paying from {shortAddress(wallet.account)}</p>
          <button onClick={pay} disabled={step !== null || tooLate}>
            {step === 'sign' ? 'Confirm in MetaMask…' : step === 'submit' ? 'Sending…' : `Pay ${checkout.amount} ${checkout.token.symbol}`}
          </button>
        </>
      )}

      {shownError && <p className={`notice ${shownError.kind === 'rejected' ? '' : 'error'}`}>{shownError.message}</p>}
    </section>
  )
}
