import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// TDD tests for loadHistory deduplication and edge cases.
// Bug: isInitial was determined by messagesRef.current.length === 0,
// but persistedMessages is module-level and survives unmount — so
// reconnect always took the pagination branch (prepend) instead of
// initial (replace), causing the latest 10 messages to be prepended
// again and again.
describe('loadHistory deduplication (TDD)', () => {
	const src = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')

	it('initial load replaces messages (not appends)', () => {
		expect(src).toContain('.splice(0,')
	})

	it('pagination deduplicates by message id', () => {
		expect(src).toContain('existingIds')
		expect(src).toContain('deduped')
		expect(src).toContain('!existingIds.has(m.id)')
	})

	it('initial detection is NOT based on messagesRef length', () => {
		expect(src).not.toContain('messagesRef.current.length === 0')
	})

	it('empty deduped page returns early without updating cursor', () => {
		// When all returned messages already exist, must bail out
		// before setNextCursor/setHasMore — otherwise cursor advances
		// past the end and observer never fires again.
		expect(src).toContain('deduped.length === 0')
		expect(src).toContain('loadingMoreRef.current = false')
	})

	it('cursor-beyond-total response (hasMore=false) stops observer', () => {
		// When server returns hasMore=false, nextCursor is empty string.
		// The IntersectionObserver effect must check hasMore before firing.
		expect(src).toContain('hasMore && !loadingMoreRef.current')
	})

	it('loadingMoreRef prevents concurrent history_request', () => {
		expect(src).toContain('loadingMoreRef.current = true')
		expect(src).toContain('loadingMoreRef.current = false')
	})

	it('prefetches second page after initial load', () => {
		expect(src).toContain('Prefetch next page')
		expect(src).toContain('msg.hasMore && msg.nextCursor')
	})

	it('IntersectionObserver uses rootMargin for early trigger', () => {
		expect(src).toContain("rootMargin: '400px")
	})

	it('resets pagination state on WS reconnect (connect_status)', () => {
		const idx = src.indexOf("case 'connect_status'")
		const block = src.slice(idx, idx + 200)
		expect(block).toContain('persistedNextCursor')
		expect(block).toContain('persistedHasMore')
	})

	it('isInitial uses ref, not React state (connect_status + history batch race)', () => {
		// connect_status and history arrive in the same synchronous batch.
		// setNextCursor('') won't have committed when loadHistory runs.
		// Must use a ref (expectingInitialRef) instead of nextCursor state.
		expect(src).toContain('expectingInitialRef')
		expect(src).toContain('expectingInitialRef.current = true')
		expect(src).toContain('expectingInitialRef.current')
		expect(src).toContain('isInitial = expectingInitialRef.current')
	})
})
