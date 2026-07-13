import { describe, it, expect } from 'vitest'
import { parseDurationFromOutput } from './utils'

describe('parseDurationFromOutput', () => {
	it('parses JSON-encoded string with prefix', () => {
		// Engine produces: json.Marshal("[Tool spent 1.2s]" + body)
		const wire = JSON.stringify('[Tool spent 1.2s]{"output":"done"}')
		expect(parseDurationFromOutput(wire)).toBe(1.2 * 1e9)
	})

	it('parses integer-second prefix', () => {
		const wire = JSON.stringify('[Tool spent 3s]ok')
		expect(parseDurationFromOutput(wire)).toBe(3 * 1e9)
	})

	it('parses already-decoded string (no JSON wrapping)', () => {
		expect(parseDurationFromOutput('[Tool spent 0.5s]result')).toBe(0.5 * 1e9)
	})

	it('returns 0 when no prefix present', () => {
		const wire = JSON.stringify('plain output')
		expect(parseDurationFromOutput(wire)).toBe(0)
	})

	it('returns 0 for non-string input (objects from ToolWithWireFormat tools)', () => {
		expect(parseDurationFromOutput({ output: 'x' })).toBe(0)
		expect(parseDurationFromOutput(undefined)).toBe(0)
		expect(parseDurationFromOutput(null)).toBe(0)
	})

	it('returns 0 for malformed seconds value', () => {
		const wire = JSON.stringify('[Tool spent ABCs]result')
		expect(parseDurationFromOutput(wire)).toBe(0)
	})

	it('returns 0 when close bracket missing', () => {
		const wire = JSON.stringify('[Tool spent 1.2s result without bracket')
		expect(parseDurationFromOutput(wire)).toBe(0)
	})
})
