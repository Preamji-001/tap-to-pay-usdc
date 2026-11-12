#!/usr/bin/env bash
# N3: expose the local stack to the phones through one cloudflared quick tunnel.
#
#   phone ──https──▶ <random>.trycloudflare.com ──▶ Vite :5173 ──/api──▶ Go backend :8080
#
# It starts the tunnel, then writes the tunnel URL into the two git-ignored local configs:
#   - crypto-pos-base-web/.env.local   VITE_API_BASE_URL  (Vite restarts itself on change)
#   - crypto-pos-nfc/local.properties  pos.apiBaseUrl     (rebuild + reinstall the POS app)
# and prints the command that starts the backend with FRONTEND_BASE_URL / CORS_ORIGIN set to it.
# The URL changes every run; keep this running for the whole test session. Ctrl-C stops it.
set -euo pipefail

WEB_DIR=${WEB_DIR:-$HOME/frontend/crypto-pos-base-web}
NFC_DIR=${NFC_DIR:-$HOME/android/crypto-pos-nfc}

command -v cloudflared >/dev/null || { echo "cloudflared not found on PATH" >&2; exit 1; }

log=$(mktemp -t n3-tunnel)
# http2 (TCP 443) instead of the default QUIC (UDP 7844), which many networks block.
cloudflared tunnel --no-autoupdate --protocol http2 --url http://localhost:5173 >"$log" 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null; rm -f "$log"' EXIT

url=""
for _ in $(seq 60); do
  url=$(grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' "$log" | head -1 || true)
  [[ -n "$url" ]] && break
  kill -0 "$pid" 2>/dev/null || { cat "$log" >&2; exit 1; }
  sleep 1
done
[[ -n "$url" ]] || { echo "no tunnel URL after 60s:" >&2; cat "$log" >&2; exit 1; }

# The URL is printed before the tunnel is connected; wait until Cloudflare's edge accepts it.
connected=""
for _ in $(seq 45); do
  grep -q "Registered tunnel connection" "$log" && { connected=1; break; }
  kill -0 "$pid" 2>/dev/null || { cat "$log" >&2; exit 1; }
  sleep 1
done
[[ -n "$connected" ]] || { echo "tunnel never connected after 45s:" >&2; tail -20 "$log" >&2; exit 1; }

# set_key FILE KEY VALUE: replace KEY's line, or append it.
set_key() {
  local file=$1 key=$2 value=$3
  touch "$file"
  if grep -q "^${key}=" "$file"; then
    sed -i '' "s|^${key}=.*|${key}=${value}|" "$file"
  else
    printf '%s=%s\n' "$key" "$value" >>"$file"
  fi
}
set_key "$WEB_DIR/.env.local" VITE_API_BASE_URL "$url"
set_key "$NFC_DIR/local.properties" pos.apiBaseUrl "$url"

cat <<MSG

Tunnel: $url
  wrote VITE_API_BASE_URL to $WEB_DIR/.env.local
  wrote pos.apiBaseUrl     to $NFC_DIR/local.properties

Next, in other terminals:
  1. web:      cd $WEB_DIR && npm run dev
  2. backend:  cd $(pwd) && set -a && . ./.env && set +a && \\
               FRONTEND_BASE_URL=$url CORS_ORIGIN=$url go run ./cmd/server
  3. POS app:  rebuild and install on the OnePlus 6T (Android Studio ▶ Run)
  4. check:    curl -s $url/api/checkouts/co_doesnotexist   (expect a JSON 404)

MSG
wait "$pid"
