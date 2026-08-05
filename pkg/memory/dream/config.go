package dream

import (
	"os"
	"strconv"
	"time"
)

// Config controls auto-dream scheduling based on user idle time.
type Config struct {
	// IdleThreshold is how long the user must be idle (no assistant message)
	// before dream fires. Default: 2h.
	IdleThreshold time.Duration
	// DreamCooldown is the minimum gap between dream runs. Default: 6h.
	DreamCooldown time.Duration
}

// DefaultConfig returns the default dream configuration.
func DefaultConfig() Config {
	return Config{
		IdleThreshold: 2 * time.Hour,
		DreamCooldown: 6 * time.Hour,
	}
}

// IsEnabled returns whether dream is enabled.
// gbot: GBOT_AUTO_DREAM env var (default: true when unset).
func IsEnabled() bool {
	v := os.Getenv("GBOT_AUTO_DREAM")
	if v == "" {
		return true // default on
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return true // default on for unparseable values
	}
	return b
}
