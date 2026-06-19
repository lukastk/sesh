// PROTOTYPE of the daemon endpoint `GET /v1/threads/rpc?id=<threadID>` (WebSocket).
// It is shaped EXACTLY like the real daemon endpoint so porting to Go is mechanical:
//   - listens on TCP (the daemon's API surface), bearer-token auth (query param, since
//     browsers can't set WS headers — the same constraint the feature map flagged);
//   - resolves the thread id -> {agent_kind, agent_session_id} (here via the sesh API;
//     in the daemon it's a direct store read);
//   - refuses non-pi (400) and a thread with no live rpc socket (409) — loud, no fallback;
//   - bridges the per-session pi rpc unix socket <-> the WebSocket (subscribe + relay).
// Cross-machine: this runs ON the owning host (co-located with the socket) and is reached
// over its TCP API — exactly how a remote/mobile client would reach it through the daemon.
import http from 'node:http'
import net from 'node:net'
import { WebSocketServer } from 'ws'

const PORT = process.env.RPC_RELAY_PORT || 8982
const API = process.env.SESH_API || 'http://127.0.0.1:8979'
const TOKEN = process.env.SESH_API_TOKEN || 'uiexptoken123'
const SOCK_DIR = process.env.PI_SOCK_DIR || '/tmp/pi-rpc-sockets'

async function resolveThread(id) {
  const r = await fetch(`${API}/v1/threads/grid`, { headers: { Authorization: `Bearer ${TOKEN}` } })
  if (!r.ok) throw new Error(`api ${r.status}`)
  const row = (await r.json()).rows.find(x => x.id === id || x.id.startsWith(id))
  if (!row) { const e = new Error('thread not found: ' + id); e.code = 404; throw e }
  return row
}

const server = http.createServer((_req, res) => { res.writeHead(404); res.end() })
const wss = new WebSocketServer({ noServer: true })

server.on('upgrade', async (req, socket, head) => {
  const url = new URL(req.url, 'http://x')
  // Real daemon path is /v1/threads/rpc; /rpc is the Vite dev-proxy alias (Vite doesn't
  // apply `rewrite` to WS upgrades, so we accept the alias too).
  if (url.pathname !== '/v1/threads/rpc' && url.pathname !== '/rpc') { socket.destroy(); return }
  // Auth: bearer token via Authorization header (non-browser clients) OR ?token= (browser WS).
  const token = (req.headers.authorization || '').replace(/^Bearer /, '') || url.searchParams.get('token')
  if (token !== TOKEN) { socket.write('HTTP/1.1 401 Unauthorized\r\n\r\n'); socket.destroy(); return }
  const id = url.searchParams.get('id')
  let row
  try { row = await resolveThread(id) }
  catch (e) { socket.write(`HTTP/1.1 ${e.code || 500} Error\r\n\r\n`); socket.destroy(); return }
  if (row.agent_kind !== 'pi') { socket.write('HTTP/1.1 400 Bad Request\r\n\r\n'); socket.destroy(); return }
  const sockPath = `${SOCK_DIR}/${row.agent_session_id}.sock`
  wss.handleUpgrade(req, socket, head, (ws) => bridge(ws, sockPath))
})

function bridge(ws, sockPath) {
  const sock = net.createConnection(sockPath)
  let buf = ''
  sock.on('connect', () => {
    sock.write(JSON.stringify({ subscribe: true }) + '\n')
    sock.write(JSON.stringify({ getState: true }) + '\n')
  })
  sock.on('data', (chunk) => {
    buf += chunk.toString(); let nl
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl); buf = buf.slice(nl + 1)
      if (line.trim() && ws.readyState === 1) ws.send(line)
    }
  })
  sock.on('error', (e) => { try { ws.send(JSON.stringify({ error: 'rpc socket: ' + e.message })) } catch {}; try { ws.close() } catch {} })
  sock.on('close', () => { try { ws.close() } catch {} })
  ws.on('message', (m) => { try { sock.write(m.toString().trim() + '\n') } catch {} })
  ws.on('close', () => { try { sock.end() } catch {} })
}

server.listen(PORT, '127.0.0.1', () => console.log(`[rpc-relay] ws //127.0.0.1:${PORT}/v1/threads/rpc?id=&token=  (api ${API})`))
