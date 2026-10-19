package mcp

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Re-exec'd helper mode: serve as a minimal MCP stdio server and exit
	// before any test selection or goleak verification.
	if os.Getenv("GBOT_TEST_MCP_HELPER") == "1" {
		helperMain()
		return // unreachable: helperMain exits the process
	}
	goleak.VerifyTestMain(m,
		// The SDK's streamable SERVER side leaves its Read goroutine parked
		// after a force-closed HTTP connection (no shutdown API). Only the
		// death-recovery test harness runs an SDK server; gbot production
		// code is client-side only.
		goleak.IgnoreTopFunction("github.com/modelcontextprotocol/go-sdk/mcp.(*streamableServerConn).Read"),
	)
}
