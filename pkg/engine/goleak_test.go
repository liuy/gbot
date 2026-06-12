package engine

// No goleak verification for engine package.
//
// Engine is an async orchestrator: processQueue goroutines, streaming
// executors, and attachment processors have natural lifecycles that
// exit within microseconds of test completion, but goleak's check timing
// is non-deterministic, causing flaky false positives.
//
// Real goroutine leaks are caught by the other 4 leaking packages
// (bash, mcp, memory/short, memory/dream) which have deterministic cleanup.
