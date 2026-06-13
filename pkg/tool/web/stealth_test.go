package web

import (
	"strings"
	"testing"
)

func TestBuildStealthInjectionScript(t *testing.T) {
	script := buildStealthInjectionScript()
	if script == "" {
		t.Fatal("expected non-empty stealth script")
	}
	// Should contain the iframe wrapper pattern.
	if !strings.Contains(script, "document.createElement") {
		t.Error("expected stealth script to contain iframe creation")
	}
	// Should contain content from at least one stealth file.
	if !strings.Contains(script, "try {") {
		t.Error("expected stealth script to contain try-catch wrappers")
	}
	// Calling again should return the same cached result (sync.Once).
	script2 := buildStealthInjectionScript()
	if script != script2 {
		t.Error("expected cached result on second call")
	}
}

func TestBuildStealthScriptFromEmbed(t *testing.T) {
	script := buildStealthScriptFromEmbed()
	if script == "" {
		t.Fatal("expected non-empty script from embedded files")
	}
	// Must contain the outer IIFE wrapper.
	if !strings.Contains(script, "(() =>") {
		t.Error("expected IIFE wrapper in stealth script")
	}
	// Must contain native function references.
	if !strings.Contains(script, "nativeWindow") {
		t.Error("expected nativeWindow reference in stealth script")
	}
}

func TestBuildStealthWrapper(t *testing.T) {
	t.Run("empty scripts", func(t *testing.T) {
		got := buildStealthWrapper(nil)
		if !strings.Contains(got, "(() =>") {
			t.Error("expected wrapper even with no scripts")
		}
	})

	t.Run("single script", func(t *testing.T) {
		got := buildStealthWrapper([]string{"console.log('test')"})
		if !strings.Contains(got, "console.log('test')") {
			t.Error("expected script content in wrapper")
		}
		if !strings.Contains(got, "try {") {
			t.Error("expected try-catch around script")
		}
	})

	t.Run("multiple scripts", func(t *testing.T) {
		got := buildStealthWrapper([]string{"script1()", "script2()"})
		if !strings.Contains(got, "script1()") {
			t.Error("expected first script in wrapper")
		}
		if !strings.Contains(got, "script2()") {
			t.Error("expected second script in wrapper")
		}
		// Each script should be wrapped in its own try-catch.
		count := strings.Count(got, "try {")
		if count < 2 {
			t.Errorf("expected at least 2 try-catch blocks, got %d", count)
		}
	})
}
