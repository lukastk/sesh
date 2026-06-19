<script>
  import { api } from './api.js'
  let { onclose, oncreated } = $props()
  let agent = $state('pi')
  let name = $state('')
  let cwd = $state('~')
  let headless = $state(false)
  let msg = $state('')
  let busy = $state(false)
  let err = $state(null)

  async function create() {
    busy = true; err = null
    try {
      const req = { agent, name, cwd, headless, mode: 'yolo' }
      if (msg.trim()) req.msg = msg.trim()
      const res = await api.threadNew(req)
      oncreated(res.id)
    } catch (e) { err = String(e); busy = false }
  }
</script>

<div class="backdrop" onclick={onclose} role="presentation">
  <div class="modal" onclick={(e) => e.stopPropagation()} role="dialog">
    <h3>New thread</h3>
    <label>Agent
      <div class="seg">
        {#each ['pi', 'claude', 'codex'] as a}
          <button class:on={agent === a} onclick={() => agent = a}>{a}</button>
        {/each}
      </div>
    </label>
    <label>Name <input bind:value={name} placeholder="(optional)" /></label>
    <label>Cwd <input bind:value={cwd} placeholder="~ or /path" /></label>
    <label class="check"><input type="checkbox" bind:checked={headless} /> headless (no tmux window)</label>
    {#if !headless}<label>Initial message <input bind:value={msg} placeholder="(optional — sent once ready)" /></label>{/if}
    {#if err}<div class="err">{err}</div>{/if}
    <div class="actions">
      <button onclick={onclose}>Cancel</button>
      <button class="primary" onclick={create} disabled={busy}>{busy ? 'Spawning…' : 'Create'}</button>
    </div>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.55); display: flex; align-items: center; justify-content: center; z-index: 50; }
  .modal { background: #16161e; border: 1px solid #2a2b3d; border-radius: 12px; padding: 20px 22px; width: 380px; display: flex; flex-direction: column; gap: 12px; }
  h3 { margin: 0 0 4px; font-size: 16px; }
  label { display: flex; flex-direction: column; gap: 5px; font-size: 12px; color: #9aa5ce; }
  label.check { flex-direction: row; align-items: center; gap: 8px; }
  input[type=text], input:not([type]) { background: #1a1b26; color: #c0caf5; border: 1px solid #2a2b3d; border-radius: 7px; padding: 8px; font-size: 13px; }
  .seg { display: flex; border: 1px solid #2a2b3d; border-radius: 7px; overflow: hidden; }
  .seg button { flex: 1; border: 0; background: #1a1b26; color: #9aa5ce; padding: 7px; font-size: 13px; cursor: pointer; }
  .seg button.on { background: #7aa2f7; color: #11121a; font-weight: 600; }
  .err { background: #3a1c28; color: #ffb4c0; border-radius: 6px; padding: 8px; font-size: 11px; white-space: pre-wrap; }
  .actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 4px; }
  .actions button { background: #1a1b26; color: #c0caf5; border: 1px solid #2a2b3d; border-radius: 7px; padding: 7px 16px; cursor: pointer; font-size: 13px; }
  .actions .primary { background: #7aa2f7; color: #11121a; border: 0; font-weight: 600; }
</style>
