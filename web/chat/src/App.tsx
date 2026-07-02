import { WebSocketProvider } from './websocket'
import ChatInterface from './components/ChatInterface'

export default function App() {
  return (
    <WebSocketProvider>
      <div className="h-full overflow-y-auto overflow-x-hidden">
        <ChatInterface />
      </div>
    </WebSocketProvider>
  )
}
