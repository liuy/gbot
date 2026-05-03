// Package tool provides the Screen type for processing raw terminal output.
// Screen interprets control characters (\r, \n) and ANSI escape sequences,
// emitting structured line events suitable for TUI rendering.
package tool

import (
	"strings"
	"unicode/utf8"
)

// ScreenEventKind distinguishes between a new line and an in-place line update.
type ScreenEventKind int

const (
	// ScreenAppend indicates a new line was added.
	ScreenAppend ScreenEventKind = iota
	// ScreenReplace indicates the current line was overwritten (caused by \r).
	ScreenReplace
)

// ScreenEvent is emitted by Screen.Write for each logical line change.
type ScreenEvent struct {
	Kind    ScreenEventKind
	Content string // line content, may contain ANSI SGR sequences
}

// Screen processes raw terminal bytes and emits structured line events.
// It interprets carriage returns (\r) as line replacements (for progress bars)
// and newlines (\n) as line advances, while preserving ANSI SGR color codes.
//
// Design notes:
//   - \r triggers emit + reset of current line (simplified model; does not
//     support partial cell-level overwrites like "foo\rbar" → "baro").
//   - ANSI SGR sequences (\x1b[...m) are preserved in output for color rendering.
//   - Other CSI/OSC sequences are silently consumed.
//   - UTF-8 multi-byte characters are handled correctly via rune-by-rune processing.
//   - Escape sequences spanning Write() boundaries are buffered via inEscape state.
type Screen struct {
	onEvent     func(ScreenEvent)
	line        strings.Builder // current line being built
	lineEmitted bool            // true after current line was emitted at least once
	inEscape    bool            // true when mid-escape-sequence across Write() calls
	escapeBuf   []byte          // accumulates incomplete escape bytes
}

// NewScreen creates a Screen that calls onEvent for each line event.
// If onEvent is nil, events are silently discarded.
func NewScreen(onEvent func(ScreenEvent)) *Screen {
	return &Screen{onEvent: onEvent}
}

// Write processes raw bytes, interpreting control characters and ANSI sequences.
// For each logical line change, it calls onEvent with an Append or Replace event.
func (s *Screen) Write(p []byte) {
	// If we were mid-escape from a previous Write, prepend the buffered bytes.
	if s.inEscape && len(s.escapeBuf) > 0 {
		combined := make([]byte, 0, len(s.escapeBuf)+len(p))
		combined = append(combined, s.escapeBuf...)
		combined = append(combined, p...)
		s.escapeBuf = s.escapeBuf[:0]
		s.inEscape = false
		p = combined
	}

	i := 0
	for i < len(p) {
		// Try to decode a UTF-8 rune
		r, size := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8 lead byte — skip it
			i++
			continue
		}

		switch {
		case r == '\r':
			s.handleCR()
			i += size

		case r == '\n':
			s.handleLF()
			i += size

		case r == '\x1b':
			// Escape sequence: may span beyond current buffer
			consumed := s.parseEscape(p[i+1:])
			if consumed < 0 {
				// Incomplete sequence — buffer remaining bytes
				remaining := p[i:]
				s.escapeBuf = append(s.escapeBuf[:0], remaining...)
				s.inEscape = true
				return
			}
			i += 1 + consumed // 1 for ESC + consumed for the rest

		case r == '\t' || r >= 0x20:
			// Printable character (including multi-byte UTF-8)
			s.line.WriteRune(r)
			i += size

		default:
			// Other control characters (< 0x20, not \r or \n): skip
			i += size
		}
	}
}

// handleCR processes a carriage return: emit current line if non-empty, then reset.
func (s *Screen) handleCR() {
	if s.line.Len() > 0 {
		kind := ScreenAppend
		if s.lineEmitted {
			kind = ScreenReplace
		}
		s.emit(kind, s.line.String())
		s.lineEmitted = true
	}
	s.line.Reset()
}

// handleLF processes a line feed: emit current line if non-empty.
// Uses Replace if lineEmitted (content from a prior \r at this line position),
// otherwise Append. Then resets for the next line.
func (s *Screen) handleLF() {
	if s.line.Len() > 0 {
		kind := ScreenAppend
		if s.lineEmitted {
			kind = ScreenReplace
		}
		s.emit(kind, s.line.String())
	}
	s.line.Reset()
	s.lineEmitted = false
}

// emit sends a ScreenEvent to the onEvent callback.
func (s *Screen) emit(kind ScreenEventKind, content string) {
	if s.onEvent != nil {
		s.onEvent(ScreenEvent{Kind: kind, Content: content})
	}
}

// parseEscape processes bytes after an ESC character.
// Returns bytes consumed (not including ESC), or -1 if the sequence is incomplete.
func (s *Screen) parseEscape(p []byte) int {
	if len(p) == 0 {
		return -1 // incomplete: just ESC
	}

	switch p[0] {
	case '[':
		n := s.parseCSI(p[1:])
		if n < 0 {
			return -1
		}
		return 1 + n
	case ']':
		n := s.parseOSC(p[1:])
		if n < 0 {
			return -1
		}
		return 1 + n
	default:
		return 1 // bare ESC or single-char sequence — consume and discard
	}
}

// parseCSI processes a CSI sequence body (after ESC [).
// SGR sequences (final byte 'm') are preserved in the line buffer.
// All other CSI sequences are silently consumed.
// Returns bytes consumed from p, or -1 if incomplete.
func (s *Screen) parseCSI(p []byte) int {
	start := 0
	// Parameter bytes: 0x30–0x3F (digits 0-9, ;, <, =, >, ?)
	for start < len(p) && p[start] >= 0x30 && p[start] <= 0x3F {
		start++
	}
	// Intermediate bytes: 0x20–0x2F (space, !, ", etc.)
	for start < len(p) && p[start] >= 0x20 && p[start] <= 0x2F {
		start++
	}
	// Final byte: 0x40–0x7E
	if start >= len(p) {
		return -1 // incomplete: no final byte
	}
	if p[start] < 0x40 || p[start] > 0x7E {
		return start // not a valid final byte — consume what we have
	}

	finalByte := p[start]
	if finalByte == 'm' {
		// SGR (Select Graphic Rendition) — preserve for color rendering
		s.line.WriteByte('\x1b')
		s.line.WriteByte('[')
		for j := 0; j < start; j++ {
			s.line.WriteByte(p[j])
		}
		s.line.WriteByte(p[start])
	}
	// All other CSI sequences (cursor movement, clearing, etc.) are discarded.

	return start + 1
}

// parseOSC processes an OSC sequence body (after ESC ]).
// OSC sequences (window title, hyperlinks) are always discarded.
// Terminated by BEL (0x07) or ST (ESC \).
// Returns bytes consumed from p, or -1 if incomplete.
func (s *Screen) parseOSC(p []byte) int {
	for i, b := range p {
		if b == 0x07 { // BEL terminator
			return i + 1
		}
		if b == '\x1b' && i+1 < len(p) && p[i+1] == '\\' { // ST terminator
			return i + 2
		}
	}
	return -1 // incomplete — sequence not terminated
}

// Flush emits any remaining line content as an Append event.
// Should be called when the input stream ends (EOF or error).
// If an escape sequence was in progress, any buffered SGR content is appended
// to the line before flushing.
func (s *Screen) Flush() {
	// If we were mid-escape, try to recover any SGR content
	if s.inEscape && len(s.escapeBuf) > 0 {
		// Check if the buffered content looks like an SGR prefix
		buf := s.escapeBuf
		if len(buf) >= 2 && buf[0] == '\x1b' && buf[1] == '[' {
			// Looks like a partial SGR — append it as-is
			s.line.Write(buf)
		}
		s.inEscape = false
		s.escapeBuf = s.escapeBuf[:0]
	}

	if s.line.Len() > 0 && !s.lineEmitted {
		s.emit(ScreenAppend, s.line.String())
		s.line.Reset()
		s.lineEmitted = true
	}
}
