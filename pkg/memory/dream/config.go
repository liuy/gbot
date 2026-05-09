package dream

import (
	"os"
	"strconv"
)

// Config controls auto-dream scheduling.
// TS source: autoDream.ts:58-66 — AutoDreamConfig + DEFAULTS.
type Config struct {
	MinHours    int // default 24
	MinSessions int // default 5
}

// DefaultConfig returns the default dream configuration.
func DefaultConfig() Config {
	return Config{MinHours: 24, MinSessions: 5}
}

// IsEnabled returns whether dream is enabled.
// gbot: GBOT_AUTO_DREAM env var (default: true when unset).
// TS: settings.autoDreamEnabled + GrowthBook tengu_onyx_plover.
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
