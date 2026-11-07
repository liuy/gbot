package web

// The embedded fixture set is re-derived from the embed FS so the
// assertions track the real payload instead of a hardcoded copy.

import (
	"strings"
	"testing"
)

func TestSKBuildStealthScriptFromEmbedContent(t *testing.T) {
	entries, err := stealthFS.ReadDir("stealth")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".txt") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatal("no embedded stealth fixtures")
	}

	script := buildStealthScriptFromEmbed()
	// Every fixture's content and one try-catch per fixture must be present.
	for _, name := range names {
		data, err := stealthFS.ReadFile("stealth/" + name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if !strings.Contains(script, string(data)) {
			t.Errorf("script missing content of %s", name)
		}
	}
	// Fixture files contain their own "try {" / catch occurrences, so the
	// expected marker count is derived from the fixtures themselves.
	wantCatch := len(names)
	for _, name := range names {
		data, _ := stealthFS.ReadFile("stealth/" + name)
		wantCatch += strings.Count(string(data), "} catch (e) {}")
	}
	if got := strings.Count(script, "} catch (e) {}"); got != wantCatch {
		t.Errorf("catch-block count = %d, want %d", got, wantCatch)
	}
}

func TestSKBuildStealthWrapperExact(t *testing.T) {
	got := buildStealthWrapper([]string{"PAYLOAD()"})
	if !strings.Contains(got, "try {\nPAYLOAD()\n} catch (e) {}\n") {
		t.Errorf("wrapper missing exact per-script try/catch block:\n%s", got)
	}
	if got := strings.Count(buildStealthWrapper(nil), "} catch (e) {}"); got != 0 {
		t.Errorf("empty wrapper catch-block count = %d, want 0", got)
	}
	if got := strings.Count(buildStealthWrapper([]string{"A", "B"}), "} catch (e) {}"); got != 2 {
		t.Errorf("two-script wrapper catch-block count = %d, want 2", got)
	}
}
