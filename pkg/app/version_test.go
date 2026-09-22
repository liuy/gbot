package app

import (
	"strings"
	"testing"
)

func TestVersionInfo_NonEmptyDefaults(t *testing.T) {
	b := VersionInfo()
	if !strings.Contains(b, "dev") || !strings.Contains(b, "unknown") {
		t.Errorf("build identity = %q, want both dev defaults without ldflags", b)
	}
	if !strings.Contains(b, "·") {
		t.Errorf("build identity = %q, want joined \"version · commit\" form", b)
	}
}
