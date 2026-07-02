import { WebSocketProvider } from './websocket'
import ChatInterface from './components/ChatInterface'

export default function App() {
  return (
    <WebSocketProvider>
      <ChatInterface />
    </WebSocketProvider>
  )
}
