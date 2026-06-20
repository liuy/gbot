package engine

import "testing"

func TestEngine_EngineID_DefaultsToMain(t *testing.T) {
	t.Parallel()
	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
	})
	t.Cleanup(func() { eng.Close() })
	if got := eng.EngineID(); got != "main" {
		t.Errorf("EngineID() = %q, want main (default)", got)
	}
}

func TestEngine_EngineID_FromParams(t *testing.T) {
	t.Parallel()
	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
		EngineID: "e2",
	})
	t.Cleanup(func() { eng.Close() })
	if got := eng.EngineID(); got != "e2" {
		t.Errorf("EngineID() = %q, want e2", got)
	}
}

func TestEngine_SetEngineID(t *testing.T) {
	t.Parallel()
	eng := New(&Params{
		Provider: &mockProvider{},
		Model:    "test-model",
	})
	t.Cleanup(func() { eng.Close() })
	if got := eng.EngineID(); got != "main" {
		t.Fatalf("initial EngineID = %q, want main", got)
	}
	eng.SetEngineID("research")
	if got := eng.EngineID(); got != "research" {
		t.Errorf("EngineID after SetEngineID = %q, want research", got)
	}
}
