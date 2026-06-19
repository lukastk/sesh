// Spike: prove streaming bubble-chat for a HEADFUL pi thread over its rpc-socket.
// Connects to <tmpdir>/pi-rpc-sockets/<sessionId>.sock, subscribes to events,
// injects a prompt, and renders the streamed text_delta / tool events — exactly
// what a message-bubble UI would consume. Usage: node probe.mjs <sessionId> "<prompt>"
import net from 'node:net'

const sessionId = process.argv[2]
const prompt = process.argv[3] || 'In 3 short sentences, what is a terminal multiplexer?'
const sock = `/tmp/pi-rpc-sockets/${sessionId}.sock`

const conn = net.createConnection(sock)
let buf = ''
let assembled = ''
const t0 = Date.now()
let firstDeltaAt = null

function send(obj) { conn.write(JSON.stringify(obj) + '\n') }

conn.on('connect', () => {
  console.log(`[connected] ${sock}`)
  send({ subscribe: true })
  send({ getState: true })
  send({ message: prompt })
  console.log(`[sent prompt] ${JSON.stringify(prompt)}\n--- streamed events ---`)
})

conn.on('data', (chunk) => {
  buf += chunk.toString()
  let nl
  while ((nl = buf.indexOf('\n')) >= 0) {
    const line = buf.slice(0, nl); buf = buf.slice(nl + 1)
    if (!line.trim()) continue
    let m; try { m = JSON.parse(line) } catch { console.log('[raw]', line); continue }
    if (m.event === 'text_delta') {
      if (firstDeltaAt === null) { firstDeltaAt = Date.now() - t0; }
      assembled += m.delta
      process.stdout.write(m.delta)           // live streaming, char by char
    } else if (m.event === 'tool_execution_start') {
      console.log(`\n[tool ▶ ${m.toolName}]`)
    } else if (m.event === 'tool_execution_end') {
      console.log(`[tool ■ ${m.toolName}]`)
    } else if (m.event === 'agent_end') {
      const elapsed = Date.now() - t0
      console.log(`\n--- agent_end ---`)
      console.log(`[timing] first delta at ${firstDeltaAt}ms, total ${elapsed}ms`)
      console.log(`[assembled bubble] ${JSON.stringify(assembled)}`)
      conn.end(); process.exit(0)
    } else if (m.ok) {
      console.log(`[ack] ${line}`)
    } else if (m.error) {
      console.log(`[error] ${line}`)
    } else {
      console.log(`[other] ${line}`)
    }
  }
})
conn.on('error', (e) => { console.error('[socket error]', e.message); process.exit(1) })
setTimeout(() => { console.error('[timeout 60s]'); process.exit(2) }, 60000)
