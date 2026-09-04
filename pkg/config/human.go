package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// IntOrHuman holds an integer value that can be specified in human-friendly
// format in JSON: "32k", "200k", "1M", or plain number 32768.
// Zero value means unset (caller decides default via DefaultCapabilities).
type IntOrHuman int

// Int returns the integer value.
func (h IntOrHuman) Int() int { return int(h) }

// IsSet returns true if the value was explicitly set (non-zero).
func (h IntOrHuman) IsSet() bool { return h != 0 }

// MarshalJSON serializes exact k/M multiples back in the human-friendly
// form so a hand-written "1M" survives a save round-trip instead of being
// rewritten to 1048576; other values stay plain JSON numbers.
func (h IntOrHuman) MarshalJSON() ([]byte, error) {
	v := int(h)
	switch {
	case v != 0 && v%(1024*1024) == 0:
		return json.Marshal(fmt.Sprintf("%dM", v/(1024*1024)))
	case v != 0 && v%1024 == 0:
		return json.Marshal(fmt.Sprintf("%dk", v/1024))
	default:
		return json.Marshal(v)
	}
}

// UnmarshalJSON accepts a JSON number or a human-friendly string like "32k", "1M".
func (h *IntOrHuman) UnmarshalJSON(data []byte) error {
	// Try JSON number first.
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*h = IntOrHuman(n)
		return nil
	}

	// Try string.
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("IntOrHuman: expected number or string, got %s", string(data))
	}

	v, err := ParseIntOrHuman(s)
	if err != nil {
		return err
	}
	*h = IntOrHuman(v)
	return nil
}

// ParseIntOrHuman parses a human-friendly integer string.
// Supported formats: "32k", "32K", "200k", "1M", "1m", "32768".
// Units are 1024-based (k = 1024, M = 1024*1024).
func ParseIntOrHuman(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("IntOrHuman: empty string")
	}

	upper := strings.ToUpper(s)
	switch {
	case strings.HasSuffix(upper, "M"):
		numStr := s[:len(s)-1]
		n, err := strconv.Atoi(numStr)
		if err != nil {
			return 0, fmt.Errorf("IntOrHuman: invalid number %q before M", numStr)
		}
		return n * 1024 * 1024, nil
	case strings.HasSuffix(upper, "K"):
		numStr := s[:len(s)-1]
		n, err := strconv.Atoi(numStr)
		if err != nil {
			return 0, fmt.Errorf("IntOrHuman: invalid number %q before k", numStr)
		}
		return n * 1024, nil
	default:
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("IntOrHuman: invalid number %q", s)
		}
		return n, nil
	}
}
