import { mount } from 'svelte'
import App from './App.svelte'
import '@xterm/xterm/css/xterm.css'

mount(App, { target: document.getElementById('app') })
