package computer

import (
	"regexp"
	"sort"
	"strings"
)

// blockedKeyCombos is the hard-blocked key combination set, translated
// verbatim from tool.py:61-92 `_BLOCKED_KEY_COMBOS`. These are destructive
// regardless of approval level (e.g. logout kills the session the agent runs
// in). Each entry is the canonicalized key set (after alias application).
var blockedKeyCombos = []map[string]struct{}{
	{"cmd": {}, "shift": {}, "backspace": {}},       // empty trash
	{"cmd": {}, "option": {}, "backspace": {}},      // force delete
	{"cmd": {}, "ctrl": {}, "q": {}},                // lock screen
	{"cmd": {}, "shift": {}, "q": {}},               // log out
	{"cmd": {}, "option": {}, "shift": {}, "q": {}}, // force log out
	// Windows secure/session shortcuts. The Windows driver accepts Win-key
	// combos, and Alt is canonicalized to option below, so block the
	// destructive variants before any backend sees them.
	{"win": {}, "l": {}},
	{"ctrl": {}, "option": {}, "delete": {}},
	{"ctrl": {}, "option": {}, "del": {}},
	{"option": {}, "f4": {}},
}

// keyAliases mirrors tool.py:94-97 `_KEY_ALIASES`. Applied during
// canonicalization so e.g. "command" and "cmd" compare equal.
var keyAliases = map[string]string{
	"command": "cmd",
	"control": "ctrl",
	"alt":     "option",
	"⌘":       "cmd",
	"⌥":       "option",
	"windows": "win",
	"super":   "win",
	"meta":    "win",
}

// canonKeyCombo parses a key combo string (e.g. "cmd+shift+q") into the set
// of canonicalized key names. Translate of tool.py:100-103 `_canon_key_combo`:
// split on `+` (with optional surrounding whitespace), lowercase each part,
// apply keyAliases. The result is a set so order is irrelevant.
func canonKeyCombo(keys string) map[string]struct{} {
	out := make(map[string]struct{})
	for part := range strings.SplitSeq(keys, "+") {
		p := strings.TrimSpace(strings.ToLower(part))
		if p == "" {
			continue
		}
		if alias, ok := keyAliases[p]; ok {
			p = alias
		}
		out[p] = struct{}{}
	}
	return out
}

// isBlockedKeyCombo reports whether combo contains any blocked set as a
// subset, returning the matching blocked combo's canonical sorted form for
// the error message. Translate of tool.py:252-260 (the inner loop over
// `_BLOCKED_KEY_COMBOS`): `blocked.issubset(combo) and len(blocked) <= len(combo)`.
// The length guard is structurally implied by subset semantics but mirrored
// here for exact parity.
func isBlockedKeyCombo(combo map[string]struct{}) (string, bool) {
	for _, blocked := range blockedKeyCombos {
		if len(blocked) > len(combo) {
			continue
		}
		match := true
		for k := range blocked {
			if _, ok := combo[k]; !ok {
				match = false
				break
			}
		}
		if match {
			return sortedComboString(blocked), true
		}
	}
	return "", false
}

// sortedComboString renders a combo set as "[a b c]" with keys sorted, so the
// error message is deterministic — translate of Python's `sorted(blocked)`.
func sortedComboString(combo map[string]struct{}) string {
	keys := make([]string, 0, len(combo))
	for k := range combo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "[" + strings.Join(keys, " ") + "]"
}

// blockedTypePatterns is the dangerous-text regex list for the `type` action,
// translated verbatim from tool.py:107-113 `_BLOCKED_TYPE_PATTERNS`. Order
// preserved so isBlockedType's returned pattern source is stable.
var blockedTypePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)curl\s+[^|]*\|\s*bash`),
	regexp.MustCompile(`(?i)curl\s+[^|]*\|\s*sh`),
	regexp.MustCompile(`(?i)wget\s+[^|]*\|\s*bash`),
	regexp.MustCompile(`(?i)\bsudo\s+rm\s+-[rf]`),
	regexp.MustCompile(`(?i)\brm\s+-rf\s+/\s*$`),
	regexp.MustCompile(`(?i):\s*\(\)\s*\{\s*:\|:\s*&\s*\}`), // fork bomb
}

// isBlockedType returns the matching pattern's source string if text matches
// any blocked pattern, or "" otherwise. Translate of tool.py:117-121
// `_is_blocked_type`.
func isBlockedType(text string) string {
	for _, pat := range blockedTypePatterns {
		if pat.MatchString(text) {
			return pat.String()
		}
	}
	return ""
}
