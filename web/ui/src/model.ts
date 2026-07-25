export type Block =
	| { kind: 'text'; id: string; text: string }
	| { kind: 'user'; id: string; text: string }  // TUI BlockUser — queued msg visual marker
	| { kind: 'image'; id: string; src: string }  // data URL or blob URL — attachments + history thumbnails
	| { kind: 'thinking'; id: string; text: string; durationNs: number; active: boolean; startedAt: number }
	| {
			kind: 'tool'
			id: string
			name: string
			summary: string
			isSearch: boolean
			isRead: boolean
			isList: boolean
			isLsp: boolean
			isWeb: boolean
			state: 'running' | 'done' | 'error'
			timingNs: number
			displayOutput: string
			startedAt: number
			children: Block[]
	  }

export type ChatMessage = {
	id: string
	role: 'user' | 'assistant'
	blocks: Block[]
	usage: {
		inputTokens: number
		outputTokens: number
		cacheRead: number
		cacheCreation: number
	}
	error: string
	status: 'streaming' | 'done'
	startedAt: number
}

export function newAssistantMessage(id: string): ChatMessage {
	return {
		id,
		role: 'assistant',
		blocks: [],
		usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
		error: '',
		status: 'streaming',
		startedAt: Date.now(),
	}
}
