// Bridge: browser bubble UI <-> a headful pi thread's rpc-socket.
// GET /                 -> the bubble page
// WS  /rpc?session=<id> -> connect to /tmp/pi-rpc-sockets/<id>.sock, subscribe,
//                          relay {event:...} frames to the browser, accept {message} to send.
import http from 'node:http'
import net from 'node:net'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { WebSocketServer } from 'ws'

const __dirname = dirname(fileURLToPath(import.meta.url))
const PORT = process.env.RPC_BRIDGE_PORT || 8981

const server = http.createServer((req, res) => {
  if (req.url.split('?')[0] === '/') {
    res.writeHead(200, { 'Content-Type': 'text/html' })
    res.end(readFileSync(join(__dirname, 'index.html')))
  } else { res.writeHead(404); res.end() }
})

const wss = new WebSocketServer({ server, path: '/rpc' })
wss.on('connection', (ws, req) => {
  const sessionId = new URL(req.url, 'http://x').searchParams.get('session')
  const sockPath = `/tmp/pi-rpc-sockets/${sessionId}.sock`
  const sock = net.createConnection(sockPath)
  let buf = ''
  sock.on('connect', () => {
    sock.write(JSON.stringify({ subscribe: true }) + '\n')
    sock.write(JSON.stringify({ getState: true }) + '\n')
  })
  sock.on('data', (chunk) => {
    buf += chunk.toString()
    let nl
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl); buf = buf.slice(nl + 1)
      if (line.trim() && ws.readyState === 1) ws.send(line)
    }
  })
  sock.on('error', (e) => { try { ws.send(JSON.stringify({ error: e.message })) } catch {} })
  // Browser -> pi: {message:"..."} (delivered as steer into the live conversation)
  ws.on('message', (m) => { try { sock.write(m.toString().trim() + '\n') } catch {} })
  ws.on('close', () => { try { sock.end() } catch {} })
})

server.listen(PORT, '127.0.0.1', () => console.log(`[rpc-bridge] http+ws on http://127.0.0.1:${PORT}`))
