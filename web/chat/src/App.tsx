import { WebSocketProvider } from './websocket'
import ChatInterface from './components/ChatInterface'

export default function App() {
  return (
    <WebSocketProvider>
      <div className="overflow-y-auto overflow-x-hidden" style={{ height: '100dvh' }}>
        <ChatInterface />
      </div>
    </WebSocketProvider>
  )
}
