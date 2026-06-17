// Package quota queries provider quota usage (zhipu / minimax / etc).
//
// Used by the TUI status bar to display "剩 X%/倒计时" next to the model name.
// Updates fire async after each main query_end to avoid blocking the TUI loop.
package quota

import (
	"context"
	"time"
)

// Info holds the parsed quota state for one window.
// Used is the percentage of quota already consumed (0-100).
// Remaining = 100 - Used. ResetAt is when the window rolls over.
type Info struct {
	Used    int       // percentage already consumed, 0-100
	ResetAt time.Time // when the 5h window rolls over
}

// Remaining returns the unused percentage (100 - Used).
func (i Info) Remaining() int { return 100 - i.Used }

// Fetcher queries a provider's quota endpoint.
type Fetcher interface {
	// Fetch returns the current 5-hour window quota, or an error.
	// Cancellation via ctx must be respected.
	Fetch(ctx context.Context) (Info, error)
}
