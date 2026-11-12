# N3: NFC tap → MetaMask handoff

## What N3 proves

The POS phone (OnePlus 6T) acts as an NFC tag holding
`https://metamask.app.link/dapp/<tunnel-host>/checkout/co_…`. The iPhone reads it, MetaMask opens our
checkout page in its in-app browser, the customer signs, and the backend answers **202**.
Nothing gets relayed yet (the relayer has no ETH until N6), so after 202 the checkout stays in
`submitting`. That's expected here.

## Why a tunnel

The phones can't reach `localhost` on the Mac, and MetaMask's dapp links need HTTPS. A cloudflared
**quick tunnel** gives a temporary public `https://<random>.trycloudflare.com` address that forwards to the
Mac. No account is needed, but the address changes on every run.

```
iPhone / 6T ──https──▶ <random>.trycloudflare.com ──▶ Vite :5173 ──/api/*──▶ Go :8080
```

Vite forwards `/api` to the backend (`vite.config.ts` → `server.proxy`), so one tunnel serves both the page
and the API. The page and API share an origin, so no CORS is needed between them. Vite only accepts
`*.trycloudflare.com` as an extra host name (`allowedHosts`).

Note: anyone who learns the tunnel URL can reach your dev stack while it runs. Creating a checkout still
needs the merchant API key. Stop the tunnel when you're done.

## Run it

Prerequisites: Docker Desktop running (`docker compose up -d`), `cloudflared` on PATH
(`~/.local/bin/cloudflared`), and the MetaMask account on the iPhone holding **Sepolia Circle USDC** ≥ the
amount (otherwise you get 422 `insufficient_balance`). The customer needs no ETH.

1. Terminal 1: `./scripts/n3-tunnel.sh`. It prints the tunnel URL and writes it into the web
   `.env.local` and the POS `local.properties`. Keep it running.
2. Terminal 2: `cd ~/frontend/crypto-pos-base-web && npm run dev`
3. Terminal 3: the backend command that the script prints (it sets `FRONTEND_BASE_URL` and `CORS_ORIGIN`
   to the tunnel).
4. Check: `curl -s <tunnel>/api/checkouts/co_x` should return a JSON `not_found`.
5. Android Studio: rebuild and install on the 6T. `pos.apiBaseUrl` changed, and it's baked in at build time.
6. On the 6T: enter an amount → the "waiting for tap" screen. Tap the iPhone (unlocked, screen on) to the 6T's back.

Checks without NFC:
- Open `<tunnel>/checkout/co_…` in iPhone Safari. The page should load.
- Open `https://metamask.app.link/dapp/<tunnel-host>/checkout/co_…` in Safari to test the MetaMask
  handoff without a tap.

## Findings so far (2026-11-09, iPhone 15 + MetaMask mobile, links opened by hand; NFC tap not yet tested)

- **Handoff works:** `metamask.app.link/dapp/<host>/checkout/co_…` opens MetaMask's in-app browser on the
  checkout page. MetaMask announces itself over EIP-6963 (`io.metamask.mobile`).
- **First real Sepolia payment:** `co_hnNSsDMG5rPYheRHjEHA2g`, 0.01 USDC, `confirmed`, tx
  `0xd4bab3d63a3dc2c1822ab0c76e74f545588b405c6276ef64ee1c3094030965ba`. The relayer paid the gas; the customer needed no ETH.
- **MetaMask mobile quirk (the big one):** for a site that has never been connected, MetaMask still answers
  `eth_accounts` with the selected account, and `eth_requestAccounts` resolves at once (the Connect sheet flashes
  and closes). `wallet_getPermissions` even lists the account and Sepolia. Yet **every** signing method
  (`personal_sign`, `eth_signTypedData_v4`) fails with `-32602 Invalid parameters: must provide an Ethereum address`.
  It is not letter case, the network, the deep link, or our typed data (MetaMask's test dapp signs fine on Sepolia).
  **Fix that worked:** connect the site from the **top-right icon** of the MetaMask browser, then Pay.
  The page now tries to recover on its own (revoke + re-request the permission, retry once) and otherwise
  tells the customer to connect from that icon. Whether the automatic recovery works is **not verified yet**.
- **MetaMask flags the payment signature as high risk** ("Review alert", red button, advises to deny). Reason: a
  TransferWithAuthorization signature lets someone else move the signer's tokens without a transaction, which is
  also how wallet-drainer phishing works; on top of that, the site is an unknown temporary `*.trycloudflare.com`
  host. Checked on chain: the signed payment moved exactly 10000 (0.01 USDC) from the payer to the merchant and
  nothing else. **UX impact: severe.** A real customer sees "deny this" at the moment of paying. A permanent,
  reputable domain may soften it but the signature type will likely stay flagged. This counts against the
  MetaMask flow and for an NFC signing-card mode.

## Findings sheet (fill in during the tap test, don't round up)

| # | Step | What happened (screens, prompts, taps) | Time |
|---|---|---|---|
| 1 | Tap iPhone on 6T | | |
| 2 | iOS banner / notification | | |
| 3 | MetaMask opens? (or Safari?) | | |
| 4 | Unlock MetaMask | | |
| 5 | Page loads the checkout | | |
| 6 | Wallet found: EIP-6963 or `window.ethereum` fallback? | | |
| 7 | Connect / account prompt | | |
| 8 | Network switch to Sepolia | | |
| 9 | Typed-data screen: domain, amount, merchant readable? | | |
| 10 | Approve → page state; backend 202? | | |
| 11 | Reject path → 4001 message shown? | | |
| 12 | POS shows `submitting` | | |

**Total customer taps/screens from tap to signed:** _

**Failures / retries / flakiness (e.g. taps not read, wrong app opened):** _

Acceptance: tap → MetaMask → checkout page → typed-data screen → approve → 202, plus the reject path showing
the 4001 message.
