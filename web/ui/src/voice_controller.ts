// Voice controller: the TS-side orchestrator (D2). Owns the PTT state
// machine (reduceVoiceState), the run-batching TTS feed (one POST per
// contiguous text run, projected with renderForSpeech — never written back
// to any message), and the teardown. The mic leg is a parameterized
// SttSession — one /ws/stt connection per turn, ptt start frame — and TTS
// rides the ordered /api/tts queue. The Go side stays a dumb pipe.
// Thinking is silent (10-31 ruling: the hum played multiple times per turn
// and felt wrong).
import { getConnection } from './ws'
import type { ServerMessage, QueryEvent } from './types'
import { createTTSQueue, type TTSQueue, type TTSQueueOpts } from './voice_tts'
import { createPlayback, type Playback } from './voice_playback'
import { renderForSpeech } from './speech_renderer'
import { SttSession } from './stt'
import type { EventBlockStore } from './components/event_blocks'
import type { VoiceState } from './voice_page'

export type { VoiceState }

// ── state machine (D6: user events drive listen/idle/wait, engine events ──
// drive think/tool/speak — no pretending to think)

export type VoiceSignal =
  | { kind: 'hold'; held: boolean }
  | { kind: 'engine'; type: string }
  | { kind: 'drained' }

export function reduceVoiceState(s: VoiceState, ev: VoiceSignal): VoiceState {
  switch (ev.kind) {
    case 'hold':
      if (ev.held) return s === 'speak' ? s : 'listen' // no barge-in (v1)
      return s === 'listen' ? 'wait' : s
    case 'engine':
      switch (ev.type) {
        case 'query_start':
          return s
        case 'thinking_start':
          return 'think'
        case 'thinking_end':
        case 'tool_end':
          // The hold gesture owns listen until release — an engine event
          // must not demote it to wait mid-hold.
          return s === 'listen' ? s : 'wait'
        case 'text_start':
        case 'text_delta':
        case 'text_end':
          return 'speak'
        case 'tool_start':
          return 'tool'
        case 'query_end':
          // speak waits for the playback drain; the controller only emits
          // the 'drained' signal once everything settled.
          return s === 'speak' ? s : 'idle'
        default:
          return s
      }
    case 'drained':
      return s === 'speak' ? 'idle' : s
  }
}

// ── controller ───────────────────────────────────────────────────────────

// The mic leg's contract: hold = start a round (open connection + capture),
// release = stop it (stop frame → done → teardown), teardown = hard abort
// with no transcript. SttSession satisfies this shape. micRMS feeds the
// listen-state aura field from the capture analyser (⑤).
export interface VoiceMicLike {
  start(): Promise<void>
  stop(): Promise<void>
  teardown(): void
  micRMS?(): number
}

export interface VoiceMicOpts {
  onPartial?(text: string): void
  onDone(text: string): void
  getPromptTexts(): string[]
}

export interface VoiceConn {
  subscribe(fn: (m: ServerMessage) => void): () => void
}

export interface VoiceControllerDeps {
  conn?: VoiceConn
  createTTSQueue?: (opts: TTSQueueOpts) => TTSQueue
  createMic?: (opts: VoiceMicOpts) => VoiceMicLike
  createPlayback?: () => Playback
  getPromptTexts?: () => string[]
  // Shared event→Block truth: the controller reduces the think/tool events it
  // consumes into this store; the voice page renders from it through the
  // chat-standard block pipeline (user ruling: one pipeline, no per-face
  // rendering code). Injection over ownership — chat.ts holds the instance so
  // controller and page bind the same object.
  blockStore: EventBlockStore
  // A spoken final goes to the chat's single user-message commit point, never
  // straight to the socket: the commit point owns input history, the optimistic
  // or queued bubble and the wire frame, and a second write path would leave
  // that state behind.
  sendUtterance: (text: string) => void
  // A failed synthesis surfaces its message here: the page has no status
  // line of its own, so the chat capsule is where the text becomes
  // visible — silence is the failure mode here.
  onTTSError?(message: string): void
  // Current session id at open time — a later connect_status naming a
  // different session tears the voice face down (session switch).
  currentSessionID?: () => string | undefined
  // Fired when teardown ran for an EXTERNAL reason (disconnect, session
  // switch) rather than the page's own close — the page owner must unmount.
  onTeardown?: () => void
  onState?: (s: VoiceState) => void
  onPartial?: (text: string) => void
}

export interface VoiceController {
  open(opts?: { autoListen?: boolean }): void
  hold(held: boolean): void
  state(): VoiceState
  // Live audio level [0,1] (RMS) for the aura draw loop (0 before the
  // contexts exist).
  level(): number
  teardown(): void
}

export function createVoiceController(deps: VoiceControllerDeps): VoiceController {
  const conn: VoiceConn = deps.conn ?? (getConnection() as unknown as VoiceConn)
  const blockStore = deps.blockStore
  let st: VoiceState = 'idle'
  let ttsQueue: TTSQueue | null = null
  let mic: VoiceMicLike | null = null
  let playback: Playback | null = null
  let unsubscribe: (() => void) | null = null
  let recording = false
  // A release whose done has not settled yet owns the mic: a hold in that
  // window must not start a second round (the settling teardown would close
  // the new round's connection).
  let releasing = false
  let opened = false
  let openedSessionID: string | undefined
  let queryEnded = true
  // Raw text of the current contiguous run. qwen3-tts has no cross-request
  // context — its context is the text within ONE request — so per-sentence
  // POSTs made the engine render each sentence without its neighbors and
  // prosody drifted between sentences. The buffer waits for the run
  // boundary (text_end / tool_start / query_end) and POSTs once.
  let bodyRun = ''
  // Drain semantics: a turn that wrote audio waits for the worklet's stop
  // event; a turn with none (everything swallowed by the renderer) idles as
  // soon as its queue settles.
  let wroteAudio = false
  let drained = true
  // Set once the audio graph failed to build; drained can never fire after
  // that, so the drain gate must let rounds through.
  let audioDead = false

  const setState = (ev: VoiceSignal): void => {
    const next = reduceVoiceState(st, ev)
    if (next !== st) {
      st = next
      deps.onState?.(st)
    }
  }

  const maybeIdleAfterDrain = (): void => {
    // audioDead: the worklet never loaded, so drained can never fire —
    // without this the round wedges in speak and blocks the mic (v1 no
    // barge-in) until the page closes.
    if (queryEnded && ttsQueue && ttsQueue.inFlight() === 0 && st === 'speak' && (!wroteAudio || drained || audioDead)) {
      setState({ kind: 'drained' })
    }
  }

  // Close the current contiguous run: project the accumulated buffer and
  // enqueue ONE POST. A run whose projection is empty (whitespace, or
  // everything swallowed by the renderer) POSTs nothing.
  const flushBodyRun = (): void => {
    const speech = renderForSpeech(bodyRun).trim()
    bodyRun = ''
    if (speech !== '') ttsQueue?.enqueue(speech)
  }

  const handleEngineEvent = (ev: QueryEvent): void => {
    switch (ev.type) {
      case 'query_start':
        bodyRun = ''
        queryEnded = false
        wroteAudio = false
        drained = true
        setState({ kind: 'engine', type: ev.type })
        break
      case 'thinking_start':
        setState({ kind: 'engine', type: ev.type })
        blockStore.apply(ev)
        break
      case 'thinking_delta': {
        const delta = ev.thinking?.text
        if (!delta) break
        // Accumulation is the store reducer's job; the controller only feeds.
        blockStore.apply(ev)
        break
      }
      case 'thinking_end':
        setState({ kind: 'engine', type: ev.type })
        blockStore.apply(ev)
        break
      case 'text_start':
        setState({ kind: 'engine', type: ev.type })
        break
      case 'text_delta':
        bodyRun += ev.text ?? ''
        setState({ kind: 'engine', type: ev.type })
        break
      case 'text_end':
      case 'tool_start':
        // A tool preamble must sound BEFORE the tool runs: tool_start is a
        // run boundary just like text_end.
        flushBodyRun()
        setState({ kind: 'engine', type: ev.type })
        if (ev.type === 'tool_start') blockStore.apply(ev)
        break
      case 'tool_param_delta':
        // Params stream into the tool row's summary — render-only, no TTS
        // or state-machine role.
        blockStore.apply(ev)
        break
      case 'tool_output_delta':
        // Live output into the running row (chat streams it on the root
        // stream too) — render-only.
        blockStore.apply(ev)
        break
      case 'tool_end':
        setState({ kind: 'engine', type: ev.type })
        blockStore.apply(ev)
        break
      case 'query_end':
        flushBodyRun()
        queryEnded = true
        setState({ kind: 'engine', type: ev.type })
        maybeIdleAfterDrain()
        break
    }
  }

  const handleServerMessage = (m: ServerMessage): void => {
    if (m.type === 'event') {
      if (m.event.agent) return // root stream only
      handleEngineEvent(m.event)
      return
    }
    if (m.type === 'connect_status') {
      if (!m.connected) {
        externalTeardown()
        return
      }
      // A session switch while the voice face is open: the stored voice
      // markers belong to the old session's history, and the face must not
      // keep streaming the new session — close it (no frames; there is no
      // face-level mode to turn off).
      if (m.sessionID && openedSessionID !== undefined && m.sessionID !== openedSessionID) {
        externalTeardown()
      }
    }
  }

  // The transcript of a finished round routes to the chat's commit point
  // (deps.sendUtterance) — the same door a typed message uses, so the timeline,
  // input history and queue state all move with it. The voice:true flag and
  // the server-side marker prefix are that commit point's concern (pinned at
  // the chat level), not this controller's.
  const handleMicDone = (text: string): void => {
    if (!opened) return // a round settling after teardown sends nothing
    // An empty/whitespace/punctuation-only final (ambient noise during a
    // silent hold) is not an utterance: no message, no voice flag — the
    // round never happened.
    if (!/[^\s。，！？.,!?~—]/u.test(text)) return
    deps.sendUtterance(text)
  }

  const buildMic = (): VoiceMicLike => {
    if (deps.createMic) {
      return deps.createMic({
        onPartial: (text: string) => deps.onPartial?.(text),
        onDone: handleMicDone,
        getPromptTexts: () => deps.getPromptTexts?.() ?? [],
      })
    }
    return new SttSession({
      ptt: true,
      onText: () => {}, // voice displays partial frames only (v1 parity)
      onPartial: (text: string) => deps.onPartial?.(text),
      onDone: handleMicDone,
      onListening: () => {}, // the PTT state machine owns the display
      onStatus: () => {}, // a failed round falls back to the next hold
      getPromptTexts: () => deps.getPromptTexts?.() ?? [],
    })
  }

  const open = (opts?: { autoListen?: boolean }): void => {
    if (opened) return
    opened = true
    queryEnded = true
    playback = deps.createPlayback ? deps.createPlayback() : createPlayback()
    const createQueue = deps.createTTSQueue ?? createTTSQueue
    ttsQueue = createQueue({
      write: (pcm) => {
        wroteAudio = true
        drained = false
        playback?.write(pcm)
      },
      onError: (message) => deps.onTTSError?.(message),
      onSettled: maybeIdleAfterDrain,
      // TTS is bounded data: when every item settles, the fetch streams are
      // done — tell the worklet instead of letting silence heuristics guess.
      onEOS: () => playback?.end(),
    })
    mic = buildMic()
    openedSessionID = deps.currentSessionID?.()
    // Prime the audio graph here: open rides the mount click's user
    // activation, so the worklet is warm long before the first TTS PCM.
    // A failed prime kills audio for THIS session only — the flag describes
    // this playback, and a rejection landing after teardown/reopen belongs
    // to a session that no longer exists.
    audioDead = false
    const pb = playback
    pb.ensureAudio().catch(() => {
      if (playback !== pb) return
      audioDead = true
      deps.onTTSError?.('tts: audio unavailable')
      // A round may have settled while the rejection was in flight.
      maybeIdleAfterDrain()
    })
    // Opening sends NOTHING: the voice marking rides each final's own
    // message frame, so there is no face-level state to announce.
    unsubscribe = conn.subscribe(handleServerMessage)
    playback.onDrained(() => {
      drained = true
      maybeIdleAfterDrain()
    })
    if (opts?.autoListen) hold(true)
  }

  const hold = (held: boolean): void => {
    if (!opened) return
    if (held) {
      if (st === 'speak') return // no barge-in (v1)
      if (releasing) return // previous release still owns the mic
      setState({ kind: 'hold', held: true })
      recording = true
      void mic?.start()
      return
    }
    if (!recording) return
    recording = false
    setState({ kind: 'hold', held: false })
    releasing = true
    void mic
      ?.stop()
      .catch(() => {})
      .finally(() => {
        releasing = false
      })
  }

  const teardown = (): void => {
    if (!opened) return
    opened = false
    openedSessionID = undefined
    recording = false
    queryEnded = true
    unsubscribe?.()
    unsubscribe = null
    ttsQueue?.abort()
    ttsQueue = null
    // Hard abort, not stop(): a closing page must not deliver the pending
    // round's transcript as a message.
    mic?.teardown()
    mic = null
    releasing = false
    playback?.interrupt()
    void playback?.teardown()
    playback = null
    st = 'idle'
    deps.onState?.(st)
  }

  // External teardown entry (server-message driven): after the controller
  // unwinds, the page owner must unmount its DOM.
  const externalTeardown = (): void => {
    teardown()
    deps.onTeardown?.()
  }

  return {
    open,
    hold,
    state: () => st,
    // One aura field, two signal sources (⑤): listen follows the mic's live
    // RMS (you speak, the field swells), everything else keeps the TTS
    // playback analyser the visualizer has always consumed.
    level: () => (st === 'listen' ? mic?.micRMS?.() ?? 0 : playback?.rms() ?? 0),
    teardown,
  }
}
