<script>
  import { api } from './api.js'
  import ThreadsScreen from './ThreadsScreen.svelte'
  import TicketsBoard from './TicketsBoard.svelte'
  import MachinesScreen from './MachinesScreen.svelte'

  let screen = $state('threads')
  let daemon = $state(null)
  const tabs = [['threads', 'Threads'], ['tickets', 'Tickets'], ['machines', 'Machines']]

  $effect(() => { (async () => { try { daemon = await api.daemonStatus() } catch {} })() })
</script>

<div class="app">
  <nav>
    <div class="brand">sesh <span>ui prototype</span></div>
    <div class="tabs">
      {#each tabs as [id, label]}
        <button class:on={screen === id} onclick={() => screen = id}>{label}</button>
      {/each}
    </div>
    <div class="spacer"></div>
    {#if daemon}<div class="daemon">▢ {daemon.machine} · schema {daemon.schema} · pid {daemon.pid}</div>{/if}
  </nav>
  <div class="body">
    {#if screen === 'threads'}<ThreadsScreen />
    {:else if screen === 'tickets'}<TicketsBoard />
    {:else if screen === 'machines'}<MachinesScreen />{/if}
  </div>
</div>

<style>
  :global(body) { margin: 0; background: #11121a; color: #c0caf5; font-family: ui-sans-serif, system-ui, sans-serif; }
  .app { display: flex; flex-direction: column; height: 100vh; }
  nav { display: flex; align-items: center; gap: 18px; padding: 0 16px; height: 46px; background: #0b0c12; border-bottom: 1px solid #1f2030; flex-shrink: 0; }
  .brand { font-size: 17px; font-weight: 700; }
  .brand span { font-size: 10px; color: #565f89; font-weight: 400; }
  .tabs { display: flex; gap: 4px; }
  .tabs button { background: none; border: 0; color: #9aa5ce; padding: 6px 14px; font-size: 13px; cursor: pointer; border-radius: 7px; }
  .tabs button:hover { background: #16161e; }
  .tabs button.on { background: #1c1d2b; color: #fff; font-weight: 600; }
  .spacer { flex: 1; }
  .daemon { font-size: 11px; color: #7aa2f7; }
  .body { flex: 1; min-height: 0; }
</style>
