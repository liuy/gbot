//go:build race

package wui

import "time"

// The race detector's instrumentation overhead slows the bridge goroutines
// enough that a parked-frame hiccup legitimately outlasts the production
// 500ms grace — a wall-clock window measured against real scheduling cannot
// hold under -race. Inflate the grace and the fake's competing hold together
// (hold must stay the larger side or "upstream write failed" wins the
// terminal race instead of "stalled").
func init() {
	sttStallGrace = 2 * time.Second
	sttStallHold = 6 * time.Second
}
