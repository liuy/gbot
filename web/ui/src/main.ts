import './index.css'
import { createChat } from './chat'
import { getConnection } from './ws'

window.addEventListener('error', (e) => {
  console.error('[uncaught]', e.message, e.error?.stack ?? e.error)
})
window.addEventListener('unhandledrejection', (e) => {
  console.error('[unhandled-rejection]', e.reason?.stack ?? e.reason)
})

const rootEl = document.getElementById('root')
if (!rootEl) throw new Error('#root element not found')
const chat = createChat({ connected: getConnection().connected })
rootEl.appendChild(chat.root)
