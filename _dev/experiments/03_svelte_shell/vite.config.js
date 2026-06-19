import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Dev-only proxy. It does two jobs that a real packaged app would do differently:
//  - /api/*  -> the daemon's TCP API, injecting the bearer token. (In Electron this
//    is the main-process fetch; on Android it's CapacitorHttp. Both bypass browser CORS,
//    which the daemon does NOT currently send — see UI_SCOPING.md.)
//  - /term   -> the experiment-01 websocket pty bridge (ws upgrade).
const API = 'http://127.0.0.1:8979'
const BRIDGE = 'http://127.0.0.1:8980'
const RELAY = 'http://127.0.0.1:8982'
const TOKEN = process.env.SESH_API_TOKEN || 'uiexptoken123'

export default defineConfig({
  plugins: [svelte()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': {
        target: API,
        changeOrigin: true,
        rewrite: (p) => p.replace(/^\/api/, '/v1'),
        headers: { Authorization: `Bearer ${TOKEN}` },
      },
      // The REAL daemon terminal endpoint (xterm pty). Token injected via header.
      '/v1/threads/terminal': { target: API, ws: true, changeOrigin: true, headers: { Authorization: `Bearer ${TOKEN}` } },
      // The REAL daemon WebSocket endpoint GET /v1/threads/rpc (pi streaming chat).
      // No node relay — straight to the daemon's TCP API. The proxy injects the bearer
      // token via the Authorization header (Electron main / Android native does this in
      // prod), so the browser never holds it; the daemon's tokenAuth gates the upgrade.
      '/v1/threads/rpc': { target: API, ws: true, changeOrigin: true, headers: { Authorization: `Bearer ${TOKEN}` } },
    },
  },
})
