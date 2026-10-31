// Aura voice visualizer: the agents-ui AgentAudioVisualizerAura port — a
// 36-iteration turbulence-SDF fragment shader with bloom + tonemap (GLSL
// byte-identical to the on-device validated aura-preview artifact) driven by
// the voice state machine and live audio RMS. The uniform-per-state mapping
// follows aura-preview's five-state table (user-tuned final values).
//
// WebGL discipline (user ruling: pure shader, NO 2D fallback): one context
// per voice-page mount, explicitly released on unmount; backing store ≤512;
// ~33fps rAF cap; context loss keeps the door open (preventDefault) and
// stops drawing until restored rebuilds every GPU resource.

import type { VoiceState } from './voice_page'

// ── shaders (verbatim from aura-preview.html — do not reformat) ──────────

export const AURA_VS = `attribute vec2 aPos; void main(){ gl_Position = vec4(aPos,0.,1.); }`

export const AURA_FS = `
precision highp float;
uniform vec2 iResolution;
uniform float iTime;
uniform float uSpeed; uniform float uBlur; uniform float uScale; uniform float uShape;
uniform float uFrequency; uniform float uAmplitude; uniform float uBloom; uniform float uMix;
uniform float uSpacing; uniform float uColorShift; uniform float uVariance;
uniform float uSmoothing; uniform float uMode; uniform vec3 uColor;
const float TAU = 6.283185;

// Noise for dithering
vec2 randFibo(vec2 p) {
  p = fract(p * vec2(443.897, 441.423));
  p += dot(p, p.yx + 19.19);
  return fract((p.xx + p.yx) * p.xy);
}

// Tonemap
vec3 Tonemap(vec3 x) {
  x *= 4.0;
  return x / (1.0 + x);
}

// Luma for alpha
float luma(vec3 color) {
  return dot(color, vec3(0.299, 0.587, 0.114));
}

// RGB to HSV
vec3 rgb2hsv(vec3 c) {
  vec4 K = vec4(0.0, -1.0 / 3.0, 2.0 / 3.0, -1.0);
  vec4 p = mix(vec4(c.bg, K.wz), vec4(c.gb, K.xy), step(c.b, c.g));
  vec4 q = mix(vec4(p.xyw, c.r), vec4(c.r, p.yzx), step(p.x, c.r));
  float d = q.x - min(q.w, q.y);
  float e = 1.0e-10;
  return vec3(abs(q.z + (q.w - q.y) / (6.0 * d + e)), d / (q.x + e), q.x);
}

// HSV to RGB
vec3 hsv2rgb(vec3 c) {
  vec4 K = vec4(1.0, 2.0 / 3.0, 1.0 / 3.0, 3.0);
  vec3 p = abs(fract(c.xxx + K.xyz) * 6.0 - K.www);
  return c.z * mix(K.xxx, clamp(p - K.xxx, 0.0, 1.0), c.y);
}

// SDF shapes
float sdCircle(vec2 st, float r) {
  return length(st) - r;
}

float sdLine(vec2 p, float r) {
  float halfLen = r * 2.0;
  vec2 a = vec2(-halfLen, 0.0);
  vec2 b = vec2(halfLen, 0.0);
  vec2 pa = p - a;
  vec2 ba = b - a;
  float h = clamp(dot(pa, ba) / dot(ba, ba), 0.0, 1.0);
  return length(pa - ba * h);
}

float getSdf(vec2 st) {
  if(uShape == 1.0) return sdCircle(st, uScale);
  else if(uShape == 2.0) return sdLine(st, uScale);
  return sdCircle(st, uScale); // Default
}

vec2 turb(vec2 pos, float t, float it) {
  // Initial rotation matrix for swirl direction
  mat2 rotation = mat2(0.6, -0.25, 0.25, 0.9);
  // Secondary rotation applied each iteration (approx 53 degree rotation)
  mat2 layerRotation = mat2(0.6, -0.8, 0.8, 0.6);

  float frequency = mix(2.0, 15.0, uFrequency);
  float amplitude = uAmplitude;
  float frequencyGrowth = 1.4;
  float animTime = t * 0.1 * uSpeed;

  const int LAYERS = 4;
  for(int i = 0; i < LAYERS; i++) {
    // Calculate wave displacement for this layer
    vec2 rotatedPos = pos * rotation;
    vec2 wave = sin(frequency * rotatedPos + float(i) * animTime + it);

    // Apply displacement along rotation direction
    pos += (amplitude / frequency) * rotation[0] * wave;

    // Evolve parameters for next layer
    rotation *= layerRotation;
    amplitude *= mix(1.0, max(wave.x, wave.y), uVariance);
    frequency *= frequencyGrowth;
  }

  return pos;
}

const float ITERATIONS = 36.0;

void mainImage(out vec4 fragColor, in vec2 fragCoord) {
  vec2 uv = fragCoord / iResolution.xy;

  vec3 pp = vec3(0.0);
  vec3 bloom = vec3(0.0);
  float t = iTime * 0.5;
  vec2 pos = uv - 0.5;

  vec2 prevPos = turb(pos, t, 0.0 - 1.0 / ITERATIONS);
  float spacing = mix(1.0, TAU, uSpacing);

  for(float i = 1.0; i < ITERATIONS + 1.0; i++) {
    float iter = i / ITERATIONS;
    vec2 st = turb(pos, t, iter * spacing);
    float d = abs(getSdf(st));
    float pd = distance(st, prevPos);
    prevPos = st;
    float dynamicBlur = exp2(pd * 2.0 * 1.4426950408889634) - 1.0;
    float ds = smoothstep(0.0, uBlur * 0.05 + max(dynamicBlur * uSmoothing, 0.001), d);

    // Shift color based on iteration using uColorScale
    vec3 color = uColor;
    if(uColorShift > 0.01) {
      vec3 hsv = rgb2hsv(color);
      // Shift hue by iteration
      hsv.x = fract(hsv.x + (1.0 - iter) * uColorShift * 0.3);
      color = hsv2rgb(hsv);
    }

    float invd = 1.0 / max(d + dynamicBlur, 0.001);
    pp += (ds - 1.0) * color;
    bloom += clamp(invd, 0.0, 250.0) * color;
  }

  pp *= 1.0 / ITERATIONS;

  vec3 color;

  // Dark mode (default)
  if(uMode < 0.5) {
    // use bloom effect
    bloom = bloom / (bloom + 2e4);
    color = (-pp + bloom * 3.0 * uBloom) * 1.2;
    color += (randFibo(fragCoord).x - 0.5) / 255.0;
    color = Tonemap(color);
    float alpha = luma(color) * uMix;
    fragColor = vec4(color * uMix, alpha);
  }

  // Light mode
  else {
    // no bloom effect
    color = -pp;
    color += (randFibo(fragCoord).x - 0.5) / 255.0;

    // Preserve hue by tone mapping brightness only
    float brightness = length(color);
    vec3 direction = brightness > 0.0 ? color / brightness : color;

    // Reinhard on brightness
    float factor = 2.0;
    float mappedBrightness = (brightness * factor) / (1.0 + brightness * factor);
    color = direction * mappedBrightness;

    // Boost saturation to compensate for white background bleed-through
    // When alpha < 1.0, white bleeds through making colors look desaturated
    // So we increase saturation to maintain vibrant appearance
    float gray = dot(color, vec3(0.2, 0.5, 0.1));
    float saturationBoost = 3.0;
    color = mix(vec3(gray), color, saturationBoost);

    // Clamp between 0-1
    color = clamp(color, 0.0, 1.0);

    float alpha = mappedBrightness * clamp(uMix, 1.0, 2.0);
    fragColor = vec4(color, alpha);
  }
}
void main(){ mainImage(gl_FragColor, gl_FragCoord.xy); }`

// ── state table (user-tuned final values; pulse mirrors a↔b over pulseMs) ─

export interface AuraParams {
  speed: number
  scale: number
  amplitude: number
  frequency: number
  brightness: number
}

export type AuraStateKey = 'idle' | 'listening' | 'thinking' | 'tooling' | 'speaking'

export const AURA_STATES: Record<AuraStateKey, AuraParams & { pulse: readonly [number, number] | null; pulseMs: number }> = {
  idle: { speed: 10, scale: 0.2, amplitude: 1.2, frequency: 0.4, brightness: 1.0, pulse: null, pulseMs: 0 },
  listening: { speed: 20, scale: 0.3, amplitude: 1.0, frequency: 0.7, brightness: 1.75, pulse: [1.5, 2.0], pulseMs: 350 },
  thinking: { speed: 30, scale: 0.3, amplitude: 0.5, frequency: 1.0, brightness: 1.5, pulse: [0.5, 2.5], pulseMs: 350 },
  tooling: { speed: 40, scale: 0.28, amplitude: 0.6, frequency: 0.8, brightness: 1.6, pulse: [1.0, 2.2], pulseMs: 220 },
  speaking: { speed: 70, scale: 0.3, amplitude: 0.75, frequency: 1.25, brightness: 1.5, pulse: null, pulseMs: 0 },
}

// wait maps to idle: it never had a halo treatment in the pre-aura page, and
// think follows within moments once the engine starts reasoning.
export function auraStateFor(s: VoiceState): AuraStateKey {
  switch (s) {
    case 'listen':
      return 'listening'
    case 'think':
      return 'thinking'
    case 'tool':
      return 'tooling'
    case 'speak':
      return 'speaking'
    default:
      return 'idle'
  }
}

// tgt() (aura-preview): the state's parameters with the pulse envelope and
// volume-driven scale applied.
export function auraTarget(key: AuraStateKey, vol: number, now: number): AuraParams {
  const s = AURA_STATES[key]
  const o: AuraParams = {
    speed: s.speed,
    scale: s.scale,
    amplitude: s.amplitude,
    frequency: s.frequency,
    brightness: s.brightness,
  }
  if (s.pulse) {
    const [a, b] = s.pulse
    const ph = (now / s.pulseMs) % 2
    const k = ph < 1 ? ph : 2 - ph
    o.brightness = a + (b - a) * k
  }
  // 头像边缘 ≈0.185；说话场压过它但不空心：0.26 静 + 0.24·vol → 0.26~0.50
  if (key === 'speaking') o.scale = 0.26 + 0.24 * vol
  else if (key === 'listening') o.scale = vol > 0 ? 0.2 + 0.2 * vol : 0.2
  return o
}

// ── volume (readVol, aura-preview): time-domain RMS ×3.2 clamped to 1 ─────

export function rmsOf(td: Uint8Array): number {
  let s2 = 0
  for (let i = 0; i < td.length; i++) {
    const v = (td[i] - 128) / 128
    s2 += v * v
  }
  return Math.min(1, Math.sqrt(s2 / td.length) * 3.2)
}

// ── theme: app tokens, dark=blue glow / light=ink (user ruling) ───────────

export interface AuraTheme {
  color: number[]
  mode: number // 0 = dark (shader bloom branch), 1 = light
}

const AURA_FALLBACK_COLOR = '#00B4FF'

export function hexToRgb(hexColor: string): number[] {
  // Both forms: authored #rrggbb and the #rgb shorthand getComputedStyle
  // serializes to (Chrome returns the token as #333, not #333333).
  const short = hexColor.trim().match(/^#([0-9a-fA-F])([0-9a-fA-F])([0-9a-fA-F])$/)
  if (short) {
    const [, r, g, b] = short
    return [r + r, g + g, b + b].map((c) => parseInt(c, 16) / 255)
  }
  const rgbColor = hexColor.trim().match(/^#([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})$/)
  if (rgbColor) {
    const [, r, g, b] = rgbColor
    return [r, g, b].map((c) => parseInt(c, 16) / 255)
  }
  return hexToRgb(AURA_FALLBACK_COLOR)
}

export function readAuraTheme(doc: Document = document): AuraTheme {
  const light = doc.documentElement.dataset.theme === 'light'
  const token = window.getComputedStyle(doc.documentElement).getPropertyValue('--color-blue')
  return { color: hexToRgb(token), mode: light ? 1 : 0 }
}

// ── GL: the structural slice of WebGL the renderer touches ────────────────

export interface AuraGL {
  createShader(type: number): unknown
  shaderSource(shader: unknown, src: string): void
  compileShader(shader: unknown): void
  getShaderParameter(shader: unknown, pname: number): boolean
  getShaderInfoLog(shader: unknown): string
  createProgram(): unknown
  attachShader(program: unknown, shader: unknown): void
  linkProgram(program: unknown): void
  useProgram(program: unknown): void
  createBuffer(): unknown
  bindBuffer(target: number, buffer: unknown): void
  bufferData(target: number, data: Float32Array, usage: number): void
  getAttribLocation(program: unknown, name: string): number
  enableVertexAttribArray(index: number): void
  vertexAttribPointer(index: number, size: number, type: number, normalized: boolean, stride: number, offset: number): void
  getUniformLocation(program: unknown, name: string): unknown
  viewport(x: number, y: number, w: number, h: number): void
  uniform2f(location: unknown, x: number, y: number): void
  uniform1f(location: unknown, x: number): void
  uniform3f(location: unknown, x: number, y: number, z: number): void
  drawArrays(mode: number, first: number, count: number): void
  getExtension(name: string): { loseContext(): void } | null
  readonly VERTEX_SHADER: number
  readonly FRAGMENT_SHADER: number
  readonly COMPILE_STATUS: number
  readonly ARRAY_BUFFER: number
  readonly STATIC_DRAW: number
  readonly FLOAT: number
  readonly TRIANGLES: number
}

const AURA_UNIFORMS = [
  'iResolution',
  'iTime',
  'uSpeed',
  'uBlur',
  'uScale',
  'uShape',
  'uFrequency',
  'uAmplitude',
  'uBloom',
  'uMix',
  'uSpacing',
  'uColorShift',
  'uVariance',
  'uSmoothing',
  'uMode',
  'uColor',
] as const

const AURA_PARAM_KEYS = ['speed', 'scale', 'amplitude', 'frequency', 'brightness'] as const

export interface AuraVisualDeps {
  getGL?: (canvas: HTMLCanvasElement) => AuraGL | null
  readTheme?: () => AuraTheme
}

export interface AuraVisual {
  // One render step (caller throttles to ~33fps): lerps cur→tgt and pushes
  // uniforms. A lost/unavailable context only skips the draw.
  frame(now: number, state: VoiceState, vol: number): void
  lost(): boolean
  destroy(): void
}

export function createAuraVisual(canvas: HTMLCanvasElement, deps: AuraVisualDeps = {}): AuraVisual {
  const getGL =
    deps.getGL ??
    ((cv: HTMLCanvasElement) => cv.getContext('webgl', { alpha: true, premultipliedAlpha: true }) as unknown as AuraGL | null)
  const readTheme = deps.readTheme ?? (() => readAuraTheme())
  let gl = getGL(canvas)
  const glExt = gl ? gl.getExtension('WEBGL_lose_context') : null
  // jsdom and a dead WebView GL both yield null: draw nothing, crash nowhere.
  let glLost = !gl
  let prog: unknown = null
  let buf: unknown = null
  let locA = -1
  const U: Record<string, unknown> = {}
  let theme = readTheme()
  let lastKey: AuraStateKey | null = null
  let t0st = 0
  const cur: AuraParams = { ...auraTarget('idle', 0, 0) }

  // GPU 资源全量重建：context 恢复后 program/buffer/uniform 全是新的，必须整体重造
  const buildGL = (): void => {
    if (!gl) return
    try {
      prog = gl.createProgram()
      const sh = (type: number, src: string): unknown => {
        const o = gl!.createShader(type)
        gl!.shaderSource(o, src)
        gl!.compileShader(o)
        if (!gl!.getShaderParameter(o, gl!.COMPILE_STATUS)) {
          throw new Error(gl!.getShaderInfoLog(o) || 'aura shader compile failed')
        }
        return o
      }
      gl.attachShader(prog, sh(gl.VERTEX_SHADER, AURA_VS))
      gl.attachShader(prog, sh(gl.FRAGMENT_SHADER, AURA_FS))
      gl.linkProgram(prog)
      gl.useProgram(prog)
      buf = gl.createBuffer()
      gl.bindBuffer(gl.ARRAY_BUFFER, buf)
      gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW)
      locA = gl.getAttribLocation(prog, 'aPos')
      gl.enableVertexAttribArray(locA)
      gl.vertexAttribPointer(locA, 2, gl.FLOAT, false, 0, 0)
      for (const n of AURA_UNIFORMS) U[n] = gl.getUniformLocation(prog, n)
    } catch (err) {
      console.error('voice aura: shader init failed', err)
      glLost = true
    }
  }

  const listeners: Array<[string, (e: Event) => void]> = []
  const on = (name: string, fn: (e: Event) => void): void => {
    canvas.addEventListener(name, fn)
    listeners.push([name, fn])
  }
  if (gl) {
    // 生命周期纪律：lost 时 preventDefault 给恢复留门；restored 后全量重建
    on('webglcontextlost', (e) => {
      e.preventDefault()
      glLost = true
    })
    on('webglcontextrestored', () => {
      glLost = false
      buildGL()
    })
    buildGL()
  }

  // data-theme flips come from theme.ts (no existing observer to join).
  const themeObserver = new MutationObserver(() => {
    theme = readTheme()
  })
  themeObserver.observe(canvas.ownerDocument!.documentElement, { attributes: true, attributeFilter: ['data-theme'] })

  return {
    frame(now: number, state: VoiceState, vol: number): void {
      const key = auraStateFor(state)
      if (key !== lastKey) {
        lastKey = key
        t0st = now
      }
      const t = auraTarget(key, vol, now)
      // cur/tgt 插值（aura-preview frame）：speed 走 0.5s ease-out，其余每帧 0.12
      const a = Math.min(1, (now - t0st) / 500)
      const e = 1 - Math.pow(1 - a, 3)
      for (const k of AURA_PARAM_KEYS) cur[k] += (t[k] - cur[k]) * (k === 'speed' ? Math.max(e, 0.02) : 0.12)
      // context 丢失且未恢复：停绘等待 restored 重建
      if (!gl || glLost) return
      gl.viewport(0, 0, canvas.width, canvas.height)
      gl.uniform2f(U.iResolution, canvas.width, canvas.height)
      gl.uniform1f(U.iTime, now / 1000)
      gl.uniform1f(U.uSpeed, cur.speed)
      gl.uniform1f(U.uBlur, 0.2)
      gl.uniform1f(U.uScale, cur.scale)
      gl.uniform1f(U.uShape, 1.0)
      gl.uniform1f(U.uFrequency, cur.frequency)
      gl.uniform1f(U.uAmplitude, cur.amplitude)
      gl.uniform1f(U.uBloom, 0.0)
      // Light ink field is faint on the near-white page (user report); the
      // shader's light branch itself expects a boosted uMix — it clamps to
      // [1.0, 2.0]. 1.3x keeps the pulse breathing inside that clamp.
      gl.uniform1f(U.uMix, theme.mode === 1 ? cur.brightness * 1.3 : cur.brightness)
      gl.uniform1f(U.uSpacing, 0.5)
      gl.uniform1f(U.uColorShift, 0.05)
      gl.uniform1f(U.uVariance, 0.1)
      gl.uniform1f(U.uSmoothing, 1.0)
      gl.uniform1f(U.uMode, theme.mode)
      gl.uniform3f(U.uColor, theme.color[0], theme.color[1], theme.color[2])
      gl.drawArrays(gl.TRIANGLES, 0, 3)
    },
    lost(): boolean {
      return glLost
    },
    destroy(): void {
      themeObserver.disconnect()
      for (const [name, fn] of listeners) canvas.removeEventListener(name, fn)
      // 离页即释放 GPU + 断引用
      if (glExt && !glLost) glExt.loseContext()
      gl = null
      glLost = true
    },
  }
}
