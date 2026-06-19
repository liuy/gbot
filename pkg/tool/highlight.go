package tool

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	lru "github.com/hashicorp/golang-lru/v2"
)

// highlightCache memoizes HighlightCode results. Keyed by language + NUL + code.
// Text-delta redraws call HighlightCode repeatedly with the same tool summaries,
// and chroma tokenization is the dominant CPU consumer — cache cuts >75% of calls.
var highlightCache, _ = lru.New[string, string](512)

// HighlightCode uses chroma to syntax-highlight code for terminal output.
// If language is a known chroma lexer name (e.g. "go", "python"), it's used directly.
// Otherwise it's treated as a file path for auto-detection via extension.
func HighlightCode(code, language string) string {
	key := language + "\x00" + code
	if v, ok := highlightCache.Get(key); ok {
		return v
	}
	result := highlightUncached(code, language)
	highlightCache.Add(key, result)
	return result
}

func highlightUncached(code, language string) string {
	// Try as language name first (e.g. "go", "python" from markdown fences)
	lexer := lexers.Get(language)
	if lexer == nil {
		// Try as file path (e.g. "main.go" from Write tool)
		lexer = lexers.Match(filepath.Base(language))
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	iterator, _ := lexer.Tokenise(nil, code)

	formatter := formatters.Get("terminal256")
	style := styles.Get("monokai")

	var buf bytes.Buffer
	_ = formatter.Format(&buf, style, iterator)
	return stripColorFromLeadingWhitespace(buf.String())
}

// stripColorFromLeadingWhitespace moves ANSI color codes after leading whitespace
// on each line. Chroma colors multi-line tokens (strings, comments) as a single
// span, which paints indentation whitespace with the token's color.
func stripColorFromLeadingWhitespace(in string) string {
	lines := strings.Split(in, "\n")
	var buf strings.Builder
	buf.Grow(len(in))
	for i, line := range lines {
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(stripLeadingANSI(line))
	}
	return buf.String()
}

// stripLeadingANSI moves ANSI escape sequences after leading whitespace.
// "\x1b[...m    text" → "    \x1b[...mtext"
func stripLeadingANSI(line string) string {
	ansiEnd := 0
loop:
	for ansiEnd < len(line) {
		switch line[ansiEnd] {
		case '\x1b':
			// Skip full ANSI sequence: ESC [ ... m
			j := ansiEnd + 1
			if j < len(line) && line[j] == '[' {
				j++
				for j < len(line) && line[j] != 'm' {
					j++
				}
				if j < len(line) {
					j++ // skip 'm'
				}
			}
			ansiEnd = j
		case ' ', '\t':
			ansiEnd++
		default:
			break loop
		}
	}
	if ansiEnd == 0 {
		return line
	}

	// Collect leading ANSI codes and whitespace separately
	var codes strings.Builder
	var spaces strings.Builder
	pos := 0
	for pos < ansiEnd {
		if line[pos] == '\x1b' {
			j := pos + 1
			if j < len(line) && line[j] == '[' {
				j++
				for j < len(line) && line[j] != 'm' {
					j++
				}
				if j < len(line) {
					j++
				}
			}
			codes.WriteString(line[pos:j])
			pos = j
		} else {
			spaces.WriteByte(line[pos])
			pos++
		}
	}
	return spaces.String() + codes.String() + line[ansiEnd:]
}
