# crypto-pos-base-web

The customer's checkout page for [the backend](../backend): a
gas-sponsored USDC checkout on Sepolia. The customer only signs. The backend's relayer pays the gas.

## What the page does

`/checkout/co_…` loads the checkout from the backend and shows the amount, merchant, reference and an expiry countdown.
Then:

1. **Connect.** MetaMask is found over EIP-6963, so another wallet can't hide it by taking `window.ethereum`.
2. **Switch to Sepolia,** if MetaMask is on another network.
3. **Pay.** MetaMask shows an EIP-712 `TransferWithAuthorization` to sign. The values come from the backend's
   `typed_data` exactly. The page refuses to sign if they disagree with the checkout on screen (chain, token,
   merchant, amount).
4. The page POSTs only `{payer, signature}`. The backend rebuilds everything else from its database.
5. The page polls every 3 s until the status is final. It shows "Payment confirmed" only at `confirmed`.

The customer never sends a transaction and needs no ETH.

## Run

```bash
npm install
cp .env.example .env.local   # VITE_API_BASE_URL, default http://localhost:8080
npm run dev                  # http://localhost:5173
```

The backend's `CORS_ORIGIN` must be this page's origin (`http://localhost:5173` locally). To get a link to open,
create a checkout:

```bash
curl -s -XPOST localhost:8080/api/checkouts -H "X-API-Key: $MERCHANT_API_KEY" -d '{"amount":"1.00"}' | jq -r .checkout_url
```

## Tests

```bash
npm test        # vitest: typed data (checked against a digest computed by the Go backend), API client, error messages
npm run build   # type-check + bundle
npm run lint
```
