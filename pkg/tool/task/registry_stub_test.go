package task

import "fmt"

// stubRegistry is a simple in-memory Registry for testing.
type stubRegistry struct {
	tasks map[string]*TaskInfo
}

func newStubRegistry(tasks ...*TaskInfo) *stubRegistry {
	m := make(map[string]*TaskInfo, len(tasks))
	for _, t := range tasks {
		m[t.ID] = t
	}
	return &stubRegistry{tasks: m}
}

func (s *stubRegistry) Get(id string) (*TaskInfo, bool) {
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

func (s *stubRegistry) List() []*TaskInfo {
	var result []*TaskInfo
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
