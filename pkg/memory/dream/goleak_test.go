package dream

import (
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/liuy/gbot/pkg/memory/short"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.Cleanup(func(exitCode int) {
			// Wait for gse background goroutine started by short.NewStore.
			for range 80 {
				if short.GseReady() {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}),
	)
}
