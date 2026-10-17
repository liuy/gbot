package short

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Handover choreography tests re-exec this binary as parent/child/
	// verifier roles; dispatch happens before goleak so role processes
	// never run the suite.
	switch os.Getenv("GBOT_E1_ROLE") {
	case "parent":
		os.Exit(e1ParentMain())
	case "child":
		os.Exit(e1ChildMain())
	case "verify":
		os.Exit(e1VerifyMain())
	}
	goleak.VerifyTestMain(m)
}
