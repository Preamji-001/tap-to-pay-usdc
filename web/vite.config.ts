import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    // N3: a phone reaches this dev server through a cloudflared quick tunnel
    // (https://<random>.trycloudflare.com). Vite rejects unknown Host headers by default.
    allowedHosts: ['.trycloudflare.com'],
    // Forward API calls to the Go backend, so one tunnel serves both the page and the API
    // (same origin: no CORS between them). Set VITE_API_BASE_URL to the tunnel URL.
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
})
