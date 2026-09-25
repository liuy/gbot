package app

import (
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/liuy/gbot/pkg/connector/wui"
	"github.com/liuy/gbot/pkg/project"
)

// pidReclaimTTL: after a stepdown releases the PID lock, how long to wait
// for the replacement to claim it before re-claiming it ourselves — the
// replacement boots in ~1s, so a missing successor after this long means
// the app's spawn failed and this process stays the one true daemon.
// Var (not const) so tests can shrink it.
var pidReclaimTTL = 60 * time.Second

// steppedDown flips once this process released its PID lock for a
// replacement (stepdown). The TERM handler must then skip pidCleanup: the
// file on disk belongs to the successor now, and deleting it would leave
// a live daemon lock-less — a later cold start would double-bind beside
// it through REUSEPORT. Same discipline as upgrade.go's handedOver.
var steppedDown atomic.Bool

// supervisedStepdown is the stepdown endpoint's wiring, extracted from
// Start so the lock discipline is unit-testable: busy → refuse (lock
// stays); idle → release the lock for the imminent app-spawned
// replacement, arm the TTL re-claim, and flip steppedDown so the TERM
// handler leaves the successor's file alone.
func supervisedStepdown(busy func() wui.BusyReport, release func(), projectDir string) (bool, wui.BusyReport) {
	report := busy()
	if report.Busy {
		return true, report
	}
	steppedDown.Store(true)
	release()
	pidPath := project.PIDFile(projectDir)
	time.AfterFunc(pidReclaimTTL, func() {
		// Existence-guarded: a live replacement's freshly written file must
		// never be clobbered with our (no longer lock-holding) pid.
		if _, statErr := os.Stat(pidPath); os.IsNotExist(statErr) {
			_ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644)
		}
	})
	return false, report
}

// The Android app spawns the daemon with GBOT_SUPERVISED=1 and owns its
// whole lifecycle: restarts run as app-spawned SO_REUSEPORT overlaps
// (stop the predecessor only after the replacement answers), NOT tableflip
// handovers. A tableflip child is an orphaned grandchild — the prime
// target of Android 12+'s phantom process killer, which silently SIGKILLed
// freshly handed-over daemons whenever the app's subprocess count ran high
// (2026-09-25: restart blackouts of 26-43 s, twice).
func supervisedMode() bool { return os.Getenv("GBOT_SUPERVISED") == "1" }
