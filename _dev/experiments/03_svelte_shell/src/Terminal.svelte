<script>
  import { Terminal } from '@xterm/xterm'
  import { FitAddon } from '@xterm/addon-fit'
  import { onMount, onDestroy } from 'svelte'

  let { threadId } = $props()
  let el
  let term, fit, ws

  function connect() {
    term = new Terminal({ cursorBlink: true, fontSize: 13, fontFamily: 'ui-monospace, monospace',
      theme: { background: '#16161e', foreground: '#c0caf5' } })
    fit = new FitAddon()
    term.loadAddon(fit)
    term.open(el)
    fit.fit()
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    // The REAL daemon endpoint GET /v1/threads/terminal (xterm pty over WS); the Vite
    // proxy injects the bearer token. (Was the exp-01 node bridge at /term.)
    ws = new WebSocket(`${proto}://${location.host}/v1/threads/terminal?id=${encodeURIComponent(threadId)}&cols=${term.cols}&rows=${term.rows}`)
    ws.binaryType = 'arraybuffer'
    ws.onopen = () => ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
    ws.onmessage = (ev) => term.write(typeof ev.data === 'string' ? ev.data : new Uint8Array(ev.data))
    term.onData((d) => ws.readyState === 1 && ws.send(d))
    const ro = new ResizeObserver(() => { fit.fit(); ws.readyState === 1 && ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows })) })
    ro.observe(el)
  }
  onMount(connect)
  onDestroy(() => { try { ws?.close() } catch {} try { term?.dispose() } catch {} })
</script>

<div class="term" bind:this={el}></div>

<style>
  .term { height: 100%; width: 100%; background: #16161e; padding: 6px; box-sizing: border-box; }
</style>
