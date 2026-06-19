// Thin sesh API client. In dev everything goes through Vite's /api proxy (which
// injects the bearer token + dodges CORS). A packaged app swaps this base for the
// daemon's TCP addr and adds the Authorization header itself (Electron main / CapacitorHttp).
const BASE = '/api'

async function get(path) {
  const r = await fetch(BASE + path)
  if (!r.ok) throw new Error(`${path}: ${r.status} ${await r.text()}`)
  return r.json()
}
async function post(path, body) {
  const r = await fetch(BASE + path, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body ?? {}),
  })
  if (!r.ok) throw new Error(`${path}: ${r.status} ${await r.text()}`)
  return r.json()
}

export const api = {
  // threads
  grid: () => get('/threads/grid'),
  status: (id) => get('/threads/status?id=' + id),
  transcript: (id, tail = 100) => get(`/threads/transcript?id=${id}&tail=${tail}`),
  headlessReply: (id) => get('/threads/headless-reply?id=' + id),
  sendHeadless: (id, text) => post('/threads/send-headless', { id, text }),
  threadNew: (req) => post('/threads', req),
  resume: (id) => post('/threads/resume', { id }),
  stop: (id) => post('/threads/stop', { id }),
  archive: (id, archived) => post('/threads/archive', { id, archived }),
  rename: (id, name) => post('/threads/rename', { id, name }),
  del: (id, force) => post('/threads/delete', { id, force }),
  daemonStatus: () => get('/status'),
  // mesh / machines
  mesh: () => get('/mesh'),
  // tickets
  ticketsAll: () => get('/tickets/list-all'),
  ticketGet: (id) => get('/tickets/get?id=' + id),
  ticketCreate: (name, prompt) => post('/tickets', { name, prompt }),
  ticketSetStatus: (id, status, threadId) => post('/tickets/status', { id, status, thread_id: threadId || undefined }),
  ticketSet: (id, fields) => post('/tickets/set', { id, ...fields }),
  ticketDelete: (id) => post('/tickets/delete', { id }),
  ticketSendPrompt: (id) => post('/tickets/send-prompt', { id }),
  // blobs
  blobAdd: (name, base64) => post('/blobs', { name, data: base64 }),
}

export const TICKET_STATUSES = ['triage', 'ready', 'active', 'done', 'dropped']

// State glyphs mirror the TUI: head (●headful / ◌headless) + busy (▶busy / ·idle).
export function glyph(row) {
  const head = row.head === 'headful' ? '●' : '◌'
  const busy = row.busy === 'busy' ? '▶' : '·'
  return head + busy
}
export function stateLabel(row) {
  if (row.head === 'headful') return row.busy === 'busy' ? 'working' : 'needs input'
  return row.busy === 'busy' ? 'turn in flight' : 'idle'
}
