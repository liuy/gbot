package job

import "fmt"

// stubRegistry is a simple in-memory Registry for testing.
type stubRegistry struct {
	tasks map[string]*JobInfo
}

func newStubRegistry(tasks ...*JobInfo) *stubRegistry {
	m := make(map[string]*JobInfo, len(tasks))
	for _, t := range tasks {
		m[t.ID] = t
	}
	return &stubRegistry{tasks: m}
}

func (s *stubRegistry) Get(id string) (*JobInfo, bool) {
	t, ok := s.tasks[id]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

func (s *stubRegistry) Kill(id string) error {
	t, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("kill %q: %w", id, ErrNotFound)
	}
	t.Status = "killed"
	t.ExitCode = 137
	return nil
}

func (s *stubRegistry) List() []*JobInfo {
	var result []*JobInfo
	for _, t := range s.tasks {
		cp := *t
		result = append(result, &cp)
	}
	return result
}

func (s *stubRegistry) Wait(id string) (int, error) {
	t, ok := s.tasks[id]
	if !ok {
		return -1, fmt.Errorf("wait %q: %w", id, ErrNotFound)
	}
	return t.ExitCode, nil
}

// errorKillRegistry returns a fixed non-ErrNotFound error from Kill.
type errorKillRegistry struct {
	err string
}

func newErrorKillRegistry(err string) *errorKillRegistry {
	return &errorKillRegistry{err: err}
}

func (e *errorKillRegistry) Get(string) (*JobInfo, bool) { return nil, false }
func (e *errorKillRegistry) Kill(string) error           { return fmt.Errorf("%s", e.err) }
func (e *errorKillRegistry) List() []*JobInfo            { return nil }
func (e *errorKillRegistry) Wait(string) (int, error)    { return -1, ErrNotFound }

// cleanupStubRegistry implements Registry and cleanupAware for testing.
type cleanupStubRegistry struct {
	cleaned bool
}

func newCleanupStubRegistry() *cleanupStubRegistry { return &cleanupStubRegistry{} }

func (c *cleanupStubRegistry) Get(string) (*JobInfo, bool) { return nil, false }
func (c *cleanupStubRegistry) Kill(string) error           { return ErrNotFound }
func (c *cleanupStubRegistry) List() []*JobInfo            { return nil }
func (c *cleanupStubRegistry) Wait(string) (int, error)    { return -1, ErrNotFound }
func (c *cleanupStubRegistry) CleanupCompleted()           { c.cleaned = true }
