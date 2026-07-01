import { WebSocketProvider } from './websocket'
import Header from './components/Header'
import ChatInterface from './components/ChatInterface'

export default function App() {
  return (
    <WebSocketProvider>
      <div className="flex h-full flex-col">
        <Header />
        <div className="flex-1 overflow-hidden">
          <ChatInterface />
        </div>
      </div>
    </WebSocketProvider>
  )
}
