package computer

import (
	"regexp"
	"strings"
)

// blockedTypePatterns is the dangerous-text regex list for the `type` action,
// translated verbatim from tool.py:107-113 `_BLOCKED_TYPE_PATTERNS`. Order
// preserved so isBlockedType's returned pattern source is stable.
//
// Type text injection is still dangerous on Android (Termux, shells) so the
// blocker survives the rewrite.
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

// androidKeys is the exact set press_key accepts, mirrored from
// MobileAccessibilityService.executePressKey. Anything else is rejected before
// the wire call so a typo can't silently no-op.
var androidKeys = map[string]bool{
	"back":            true,
	"home":            true,
	"recents":         true,
	"recent":          true,
	"notifications":   true,
	"quick_settings":  true,
	"power_dialog":    true,
	"split_screen":    true,
	"lock_screen":     true,
	"take_screenshot": true,
}

// validAndroidKey reports whether key is in the press_key allowlist. The
// check is single-sited in dispatch (pre-ensureConnected) so an unknown key
// fails fast with no wire traffic and no connection requirement.
func validAndroidKey(key string) bool {
	return androidKeys[strings.ToLower(strings.TrimSpace(key))]
}
