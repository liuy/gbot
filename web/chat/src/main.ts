import './index.css'
import { createChat } from './chat'
import { getConnection } from './ws'

const rootEl = document.getElementById('root')
if (!rootEl) throw new Error('#root element not found')
const chat = createChat({ connected: getConnection().connected })
rootEl.appendChild(chat.root)
