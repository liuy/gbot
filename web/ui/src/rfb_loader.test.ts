import { describe, it, expect } from 'vitest'
import { NOVNC_MODULE_URL } from './rfb_loader'

describe('rfb_loader', () => {
  // Pins the cross-language contract: this path must match the route
  // registered in pkg/connector/wui/assets.go (GET /assets/novnc.esm.js).
  it('targets the daemon-served prebundle path', () => {
    expect(NOVNC_MODULE_URL).toBe('/assets/novnc.esm.js')
  })
})
