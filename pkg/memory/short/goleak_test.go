package short

import (
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.Cleanup(func(exitCode int) {
			// Wait for gse background goroutine to finish loading.
			// initGse runs in a goroutine via sync.Once and takes ~2-3s.
			// Without waiting, goleak sees it as a leak.
			for range 80 {
				if globalGseReady.Load() {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}),
	)
}
