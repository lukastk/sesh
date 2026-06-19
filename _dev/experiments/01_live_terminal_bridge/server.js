// Throwaway prototype: a websocket <-> pty bridge that gives a browser a real
// interactive terminal onto a sesh thread's tmux pane. Proves the "chat with a
// live thread inside the UI" path (option C in the chat-architecture deep-dive).
//
// GET /                      -> the xterm.js page
// WS  /term?thread=<id>      -> resolve the thread's pane via the @sesh-thread-id
//                              marker, then attach a pty to `tmux attach` and bridge bytes.
//
// Env (from _dev/experiments/env.sh): SESH_TMUX_SOCKET. Run: `node server.js`.
import http from 'node:http'
import { readFileSync, existsSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { WebSocketServer } from 'ws'
import pty from 'node-pty'

const __dirname = dirname(fileURLToPath(import.meta.url))
const SOCKET = process.env.SESH_TMUX_SOCKET || 'sesh-ui-exp'
const PORT = process.env.TERM_BRIDGE_PORT || 8980

function tmux(args) {
  return execFileSync('tmux', ['-L', SOCKET, ...args], { encoding: 'utf8' })
}

// Resolve a thread id -> {session, window, pane} via the pane marker (the same
// truth sesh's own runtime resolution uses).
function resolvePane(threadId) {
  const out = tmux(['list-panes', '-a', '-F',
    '#{@sesh-thread-id}\t#{session_name}\t#{window_index}\t#{pane_id}'])
  for (const line of out.split('\n')) {
    const [id, session, window, pane] = line.split('\t')
    if (id === threadId) return { session, window, pane }
  }
  return null
}

const STATIC = {
  '/': ['index.html', 'text/html'],
  '/xterm.js': ['node_modules/xterm/lib/xterm.js', 'application/javascript'],
  '/xterm.css': ['node_modules/xterm/css/xterm.css', 'text/css'],
  '/addon-fit.js': ['node_modules/xterm-addon-fit/lib/xterm-addon-fit.js', 'application/javascript'],
}

const server = http.createServer((req, res) => {
  const path = req.url.split('?')[0]
  const entry = STATIC[path]
  if (!entry) { res.writeHead(404); res.end('not found'); return }
  const file = join(__dirname, entry[0])
  if (!existsSync(file)) { res.writeHead(404); res.end('missing ' + entry[0]); return }
  res.writeHead(200, { 'Content-Type': entry[1] })
  res.end(readFileSync(file))
})

const wss = new WebSocketServer({ server, path: '/term' })
wss.on('connection', (ws, req) => {
  const url = new URL(req.url, 'http://x')
  const threadId = url.searchParams.get('thread')
  let target
  try {
    const loc = resolvePane(threadId)
    if (!loc) { ws.send('\r\n[bridge] thread ' + threadId + ' has no live pane\r\n'); ws.close(); return }
    target = loc
  } catch (e) { ws.send('\r\n[bridge] resolve error: ' + e.message + '\r\n'); ws.close(); return }

  // Attach a pty to the thread's tmux session, selecting its exact window+pane.
  // `-f ignore-size` keeps THIS client from shrinking the user's real attachment
  // (per-client sizing); we still drive the live session, not a copy.
  const term = pty.spawn('tmux', [
    '-L', SOCKET, 'attach-session', '-t', `${target.session}:${target.window}`,
  ], {
    name: 'xterm-256color',
    cols: 120, rows: 32,
    env: { ...process.env, TMUX: '' }, // unset TMUX so nested attach is allowed
  })

  term.onData((d) => { try { ws.send(d) } catch {} })
  term.onExit(() => { try { ws.close() } catch {} })

  ws.on('message', (msg, isBinary) => {
    // Control frames are JSON {type:'resize',cols,rows}; everything else is keystrokes.
    if (!isBinary) {
      const s = msg.toString()
      if (s.startsWith('{')) {
        try {
          const m = JSON.parse(s)
          if (m.type === 'resize') { term.resize(m.cols, m.rows); return }
        } catch {}
      }
      term.write(s)
      return
    }
    term.write(msg)
  })
  ws.on('close', () => { try { term.kill() } catch {} })
})

server.listen(PORT, '127.0.0.1', () => {
  console.log(`[bridge] http+ws on http://127.0.0.1:${PORT}  (tmux -L ${SOCKET})`)
})
