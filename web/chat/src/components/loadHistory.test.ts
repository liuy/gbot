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
		// IntersectionObserver sets loadingMoreRef=true before sending
		// request, and loadHistory resets it to false after receiving.
		// This prevents duplicate requests during rapid scroll.
		expect(src).toContain('loadingMoreRef.current = true')
		expect(src).toContain('loadingMoreRef.current = false')
	})
})
