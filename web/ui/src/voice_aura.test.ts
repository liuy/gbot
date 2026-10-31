import { describe, it, expect, vi } from 'vitest'
import {
  AURA_FS,
  AURA_VS,
  AURA_STATES,
  auraStateFor,
  auraTarget,
  rmsOf,
  hexToRgb,
  readAuraTheme,
  createAuraVisual,
  type AuraGL,
} from './voice_aura'
import type { VoiceState } from './voice_page'

// ── shader source existence (byte-identical port is verified against the
// artifact at authoring time; these guard the load-bearing features against
// accidental deletion) ────────────────────────────────────────────────────

describe('AURA shader sources', () => {
  it('fragment source keeps the 36-iteration turbulence core', () => {
    expect(AURA_FS).toContain('const float ITERATIONS = 36.0;')
    expect(AURA_FS).toContain('vec2 turb(vec2 pos, float t, float it)')
    expect(AURA_FS).toContain('const int LAYERS = 4;')
  })

  it('fragment source keeps BOTH uMode branches (dark bloom / light tonemap)', () => {
    expect(AURA_FS).toContain('if(uMode < 0.5)')
    expect(AURA_FS).toContain('bloom = bloom / (bloom + 2e4);') // dark
    expect(AURA_FS).toContain('float saturationBoost = 3.0;') // light
    expect(AURA_FS).toContain('clamp(uMix, 1.0, 2.0)') // light alpha
  })

  it('vertex source is the fullscreen-triangle pass-through', () => {
    expect(AURA_VS).toContain('attribute vec2 aPos;')
    expect(AURA_VS).toContain('gl_Position = vec4(aPos,0.,1.);')
  })
})

// ── state table + mapping (user-tuned final values) ───────────────────────

describe('AURA_STATES + auraStateFor', () => {
  it('carries the five tuned states exactly', () => {
    expect(AURA_STATES.idle).toEqual({
      speed: 10, scale: 0.2, amplitude: 1.2, frequency: 0.4, brightness: 1.0, pulse: null, pulseMs: 0,
    })
    expect(AURA_STATES.listening).toEqual({
      speed: 20, scale: 0.3, amplitude: 1.0, frequency: 0.7, brightness: 1.75,
      pulse: [1.5, 2.0], pulseMs: 350,
    })
    expect(AURA_STATES.thinking).toEqual({
      speed: 30, scale: 0.3, amplitude: 0.5, frequency: 1.0, brightness: 1.5,
      pulse: [0.5, 2.5], pulseMs: 350,
    })
    expect(AURA_STATES.tooling).toEqual({
      speed: 40, scale: 0.28, amplitude: 0.6, frequency: 0.8, brightness: 1.6,
      pulse: [1.0, 2.2], pulseMs: 220,
    })
    expect(AURA_STATES.speaking).toEqual({
      speed: 70, scale: 0.3, amplitude: 0.75, frequency: 1.25, brightness: 1.5, pulse: null, pulseMs: 0,
    })
  })

  it('maps every voice state (wait rides idle until think arrives)', () => {
    const cases: Array<[VoiceState, ReturnType<typeof auraStateFor>]> = [
      ['idle', 'idle'],
      ['wait', 'idle'],
      ['listen', 'listening'],
      ['think', 'thinking'],
      ['tool', 'tooling'],
      ['speak', 'speaking'],
    ]
    for (const [st, key] of cases) expect(auraStateFor(st)).toBe(key)
  })
})

describe('auraTarget', () => {
  it('idle returns the table values verbatim, volume ignored', () => {
    expect(auraTarget('idle', 0.9, 12345)).toEqual({
      speed: 10, scale: 0.2, amplitude: 1.2, frequency: 0.4, brightness: 1.0,
    })
  })

  it('speaking scale rides the TTS RMS: 0.26 + 0.24·vol', () => {
    expect(auraTarget('speaking', 0, 0).scale).toBeCloseTo(0.26, 6)
    expect(auraTarget('speaking', 0.5, 0).scale).toBeCloseTo(0.38, 6)
    expect(auraTarget('speaking', 1, 0).scale).toBeCloseTo(0.5, 6)
  })

  it('listening scale rides the mic RMS: 0.2 + 0.2·vol (0 → floor)', () => {
    expect(auraTarget('listening', 0, 0).scale).toBeCloseTo(0.2, 6)
    expect(auraTarget('listening', 0.8, 0).scale).toBeCloseTo(0.36, 6)
    expect(auraTarget('listening', 1, 0).scale).toBeCloseTo(0.4, 6)
  })

  it('think brightness breathes 0.5↔2.5 over the 350ms mirror', () => {
    expect(auraTarget('thinking', 0, 0).brightness).toBeCloseTo(0.5, 6)
    expect(auraTarget('thinking', 0, 350).brightness).toBeCloseTo(2.5, 6)
    expect(auraTarget('thinking', 0, 175).brightness).toBeCloseTo(1.5, 6)
    expect(auraTarget('thinking', 0, 700).brightness).toBeCloseTo(0.5, 6) // mirrored back
  })

  it('tool brightness pulses 1.0↔2.2 on the fast 220ms cycle', () => {
    expect(auraTarget('tooling', 0, 0).brightness).toBeCloseTo(1.0, 6)
    expect(auraTarget('tooling', 0, 110).brightness).toBeCloseTo(1.6, 6)
    expect(auraTarget('tooling', 0, 220).brightness).toBeCloseTo(2.2, 6)
    expect(auraTarget('tooling', 0, 440).brightness).toBeCloseTo(1.0, 6)
  })

  it('listening brightness pulses 1.5↔2.0; speaking stays flat 1.5', () => {
    expect(auraTarget('listening', 0, 0).brightness).toBeCloseTo(1.5, 6)
    expect(auraTarget('listening', 0, 350).brightness).toBeCloseTo(2.0, 6)
    expect(auraTarget('speaking', 0.5, 777).brightness).toBeCloseTo(1.5, 6)
  })
})

// ── volume math (readVol, aura-preview) ───────────────────────────────────

describe('rmsOf', () => {
  it('silence (all 128) reads 0', () => {
    expect(rmsOf(new Uint8Array(1024).fill(128))).toBe(0)
  })

  it('loud speech clamps to 1 (RMS ×3.2 gain)', () => {
    // ±64 around 128 → |v| = 0.5 → 0.5×3.2 = 1.6 → clamped 1.
    const td = new Uint8Array(1024)
    for (let i = 0; i < td.length; i++) td[i] = i % 2 === 0 ? 192 : 64
    expect(rmsOf(td)).toBe(1)
  })

  it('conversational RMS lands inside (0,1)', () => {
    // ±18 around 128 → |v| = 0.140625 → ×3.2 = 0.45.
    const td = new Uint8Array(1024)
    for (let i = 0; i < td.length; i++) td[i] = i % 2 === 0 ? 146 : 110
    expect(rmsOf(td)).toBeCloseTo(0.45, 5)
  })
})

// ── theme: app tokens → uColor/uMode ──────────────────────────────────────

describe('hexToRgb / readAuraTheme', () => {
  it('parses #rrggbb into normalized rgb', () => {
    expect(hexToRgb('#00B4FF')).toEqual([0, 180 / 255, 1])
    expect(hexToRgb('#333333')).toEqual([0.2, 0.2, 0.2]) // 51/255 = 0.2 exactly
    expect(hexToRgb(' #00B4FF ')).toEqual([0, 180 / 255, 1]) // CSS var whitespace
  })

  it('parses #rgb shorthand — getComputedStyle serializes #333333 as #333', () => {
    // Real-Chrome probe (bundle CSS, headless): the light --color-blue token
    // arrives as the 3-digit shorthand, and the 6-digit-only parse silently
    // fell back to blue — the "aura ignores the theme" device report.
    expect(hexToRgb('#333')).toEqual([0.2, 0.2, 0.2])
    expect(hexToRgb('#FFF')).toEqual([1, 1, 1])
  })

  it('non-hex tokens fall back to the app blue', () => {
    expect(hexToRgb('')).toEqual([0, 180 / 255, 1])
    expect(hexToRgb('var(--x)')).toEqual([0, 180 / 255, 1])
  })

  it('dark token → bloom branch (mode 0, blue); light token → light branch (mode 1, ink)', () => {
    const spy = vi.spyOn(window, 'getComputedStyle').mockImplementation(
      (() => ({
        getPropertyValue: (p: string) => (p === '--color-blue' ? '#00B4FF' : ''),
      })) as unknown as typeof window.getComputedStyle,
    )
    delete document.documentElement.dataset.theme
    const dark = readAuraTheme()
    expect(dark.mode).toBe(0)
    expect(dark.color).toEqual([0, 180 / 255, 1])

    spy.mockImplementation(
      (() => ({
        getPropertyValue: (p: string) => (p === '--color-blue' ? '#333' : ''),
      })) as unknown as typeof window.getComputedStyle,
    )
    document.documentElement.dataset.theme = 'light'
    const light = readAuraTheme()
    expect(light.mode).toBe(1)
    expect(light.color).toEqual([0.2, 0.2, 0.2])
    spy.mockRestore()
    delete document.documentElement.dataset.theme
  })
})

// ── GL lifecycle state machine (thin interface; jsdom has zero WebGL) ─────

class FakeGL {
  programs = 0
  draws = 0
  uniforms: Record<string, number[]> = {}
  lostExt = { loseContext: vi.fn() }
  private locs: Record<string, unknown> = {}
  readonly VERTEX_SHADER = 1
  readonly FRAGMENT_SHADER = 2
  readonly COMPILE_STATUS = 3
  readonly ARRAY_BUFFER = 4
  readonly STATIC_DRAW = 5
  readonly FLOAT = 6
  readonly TRIANGLES = 7
  createProgram() {
    this.programs++
    return { p: this.programs }
  }
  createShader() {
    return { s: true }
  }
  shaderSource() {}
  compileShader() {}
  getShaderParameter() {
    return true
  }
  getShaderInfoLog() {
    return ''
  }
  attachShader() {}
  linkProgram() {}
  useProgram() {}
  createBuffer() {
    return { b: 1 }
  }
  bindBuffer() {}
  bufferData() {}
  getAttribLocation() {
    return 0
  }
  enableVertexAttribArray() {}
  vertexAttribPointer() {}
  getUniformLocation(_p: unknown, name: string) {
    this.locs[name] ??= { name }
    return this.locs[name]
  }
  viewport() {}
  uniform2f(loc: { name: string }, x: number, y: number) {
    this.uniforms[loc.name] = [x, y]
  }
  uniform1f(loc: { name: string }, x: number) {
    this.uniforms[loc.name] = [x]
  }
  uniform3f(loc: { name: string }, x: number, y: number, z: number) {
    this.uniforms[loc.name] = [x, y, z]
  }
  drawArrays() {
    this.draws++
  }
  getExtension(name: string) {
    return name === 'WEBGL_lose_context' ? this.lostExt : null
  }
}

function makeVisual(opts: { gl?: FakeGL | null } = {}) {
  const canvas = document.createElement('canvas')
  const gl = opts.gl === undefined ? new FakeGL() : opts.gl
  const visual = createAuraVisual(canvas, {
    getGL: () => gl as unknown as AuraGL | null,
    readTheme: () => ({ color: [0, 180 / 255, 1], mode: 0 }),
  })
  return { canvas, visual, gl: gl as FakeGL }
}

describe('createAuraVisual', () => {
  it('builds GPU resources once at creation and draws per frame with the fixed uniforms', () => {
    const { visual, gl } = makeVisual()
    expect(gl.programs).toBe(1)
    visual.frame(0, 'idle', 0)
    visual.frame(33, 'idle', 0)
    expect(gl.draws).toBe(2)
    expect(gl.uniforms.uSpeed).toEqual([10])
    expect(gl.uniforms.uBlur).toEqual([0.2])
    expect(gl.uniforms.uShape).toEqual([1.0])
    expect(gl.uniforms.uBloom).toEqual([0.0])
    expect(gl.uniforms.uSpacing).toEqual([0.5])
    expect(gl.uniforms.uColorShift).toEqual([0.05])
    expect(gl.uniforms.uVariance).toEqual([0.1])
    expect(gl.uniforms.uSmoothing).toEqual([1.0])
    expect(gl.uniforms.uMode).toEqual([0])
    expect(gl.uniforms.uColor).toEqual([0, 180 / 255, 1])
    visual.destroy()
  })

  it('transitions: speed eases over 0.5s, the rest lerps 0.12 per frame', () => {
    const { visual, gl } = makeVisual()
    visual.frame(0, 'idle', 0)
    visual.frame(1000, 'speak', 0.5) // state change at 1000: ease factor 0
    // speed: 10 + (70-10)*0.02 = 11.2; scale: 0.2 + (0.38-0.2)*0.12 = 0.2216
    expect(gl.uniforms.uSpeed[0]).toBeCloseTo(11.2, 6)
    expect(gl.uniforms.uScale[0]).toBeCloseTo(0.2216, 6)
    expect(gl.uniforms.uMix[0]).toBeCloseTo(1.0 + 0.5 * 0.12, 6) // brightness
    visual.frame(2000, 'speak', 0.5) // 1000ms in state: ease done
    expect(gl.uniforms.uSpeed[0]).toBeCloseTo(70, 6)
    expect(gl.uniforms.uScale[0]).toBeCloseTo(0.2216 + (0.38 - 0.2216) * 0.12, 6)
    visual.destroy()
  })

  it('listen frame pushes the mic-RMS-driven scale', () => {
    const { visual, gl } = makeVisual()
    visual.frame(0, 'listen', 0.8)
    // Enough frames for the 0.12 lerp to lock onto the target.
    for (let i = 1; i < 60; i++) visual.frame(i * 33, 'listen', 0.8)
    expect(gl.uniforms.uScale[0]).toBeCloseTo(0.36, 3)
    expect(gl.uniforms.uSpeed[0]).toBeCloseTo(20, 6)
    visual.destroy()
  })

  it('null GL (jsdom / dead WebView): creation and frames never throw, nothing draws', () => {
    const { visual } = makeVisual({ gl: null })
    expect(visual.lost()).toBe(true)
    expect(() => {
      visual.frame(0, 'speak', 0.5)
      visual.frame(33, 'speak', 0.5)
    }).not.toThrow()
    visual.destroy()
  })

  it('contextlost stops drawing (door held open); restored rebuilds ALL GPU resources', () => {
    const { canvas, visual, gl } = makeVisual()
    visual.frame(0, 'idle', 0)
    expect(gl.draws).toBe(1)
    const ev = new Event('webglcontextlost')
    const prevent = vi.spyOn(ev, 'preventDefault')
    canvas.dispatchEvent(ev)
    expect(prevent).toHaveBeenCalledTimes(1)
    expect(visual.lost()).toBe(true)
    visual.frame(33, 'idle', 0)
    expect(gl.draws).toBe(1) // stopped — no 2D fallback, no new context
    canvas.dispatchEvent(new Event('webglcontextrestored'))
    expect(visual.lost()).toBe(false)
    expect(gl.programs).toBe(2) // full buildGL: program/buffer/uniforms anew
    visual.frame(66, 'idle', 0)
    expect(gl.draws).toBe(2)
    visual.destroy()
  })

  it('destroy releases the context and stops drawing', () => {
    const { visual, gl } = makeVisual()
    expect(gl.lostExt.loseContext).not.toHaveBeenCalled()
    visual.destroy()
    expect(gl.lostExt.loseContext).toHaveBeenCalledTimes(1)
    expect(visual.lost()).toBe(true)
    const draws = gl.draws
    visual.frame(0, 'idle', 0)
    expect(gl.draws).toBe(draws)
  })

  it('data-theme flips re-read the theme into uMode/uColor', async () => {
    const canvas = document.createElement('canvas')
    document.body.appendChild(canvas)
    let token = '#00B4FF'
    let light = false
    const gl = new FakeGL()
    const visual = createAuraVisual(canvas, {
      getGL: () => gl as unknown as AuraGL,
      readTheme: () => ({ color: hexToRgb(token), mode: light ? 1 : 0 }),
    })
    visual.frame(0, 'idle', 0)
    expect(gl.uniforms.uMode).toEqual([0])
    expect(gl.uniforms.uColor).toEqual([0, 180 / 255, 1])
    token = '#333333'
    light = true
    document.documentElement.dataset.theme = 'light'
    // MutationObserver callbacks land on a microtask — retry until the next
    // frame picks up the re-read theme.
    await vi.waitFor(() => {
      visual.frame(33, 'idle', 0)
      expect(gl.uniforms.uMode).toEqual([1])
      expect(gl.uniforms.uColor).toEqual([0.2, 0.2, 0.2])
    })
    delete document.documentElement.dataset.theme
    canvas.remove()
    visual.destroy()
  })
})
