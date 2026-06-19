// Detach-safety characterization: when the UI attaches a 2nd tmux client to a session
// the user is ALSO attached to, does it resize/disturb the user's view? And does
// `window-size largest` prevent a smaller UI client from shrinking the user's bigger one?
// Uses node-pty (from ../01_live_terminal_bridge) to spawn real, sized tmux clients.
import pty from '../01_live_terminal_bridge/node_modules/node-pty/lib/index.js'
import { execFileSync } from 'node:child_process'

const SOCK = 'sesh-detach-test'
const tmux = (...a) => execFileSync('tmux', ['-L', SOCK, ...a], { encoding: 'utf8' }).trim()
const winSize = () => tmux('display-message', '-p', '-t', 'work', '#{window_width}x#{window_height}')
const clients = () => tmux('list-clients', '-t', 'work', '-F', '#{client_name} #{client_width}x#{client_height}').split('\n').filter(Boolean)

function attach(cols, rows) {
  const p = pty.spawn('tmux', ['-L', SOCK, 'attach-session', '-t', 'work'],
    { name: 'xterm-256color', cols, rows, env: { ...process.env, TMUX: '' } })
  p.onData(() => {}) // drain
  return p
}
const sleep = (ms) => new Promise(r => setTimeout(r, ms))

async function run(label, setup) {
  try { tmux('kill-server') } catch {}
  await sleep(200)
  // A big session; window-size default (latest) unless setup changes it.
  tmux('new-session', '-d', '-s', 'work', '-x', '220', '-y', '55')
  if (setup) setup()
  const user = attach(200, 50); await sleep(400)
  const beforeUI = winSize()
  const ui = attach(90, 28); await sleep(600)
  const afterUI = winSize()
  console.log(`\n=== ${label} ===`)
  console.log(`window after USER (200x50) attaches: ${beforeUI}`)
  console.log(`window after UI   (90x28) attaches:  ${afterUI}`)
  console.log(`clients:`, clients())
  console.log(afterUI === beforeUI
    ? `✓ SAFE: the UI attach did NOT change the window the user sees`
    : `✗ DISTURBED: the window changed (${beforeUI} -> ${afterUI})`)
  ui.kill(); user.kill(); await sleep(200)
}

await run('DEFAULT (window-size latest)', null)
await run('window-size largest', () => tmux('set-option', '-g', 'window-size', 'largest'))
await run('UI attaches READ-ONLY (-r) + largest', () => tmux('set-option', '-g', 'window-size', 'largest'))
try { tmux('kill-server') } catch {}
console.log('\n(done)')
process.exit(0)
