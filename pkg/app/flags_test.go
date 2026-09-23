package app

import (
	"runtime"
	"testing"
)

func TestParseFlags_Default(t *testing.T) {
	opts := ParseFlags(nil)
	if opts.WSPort != "8765" {
		t.Errorf("WSPort = %q, want 8765", opts.WSPort)
	}
	if runtime.GOOS == "android" {
		if !opts.DaemonMode {
			t.Errorf("DaemonMode = false, want true on android")
		}
		if !opts.NoTUI {
			t.Errorf("NoTUI = false, want true on android (no terminal — a bubbletea loop would fail and wui would never mount)")
		}
	} else {
		if opts.DaemonMode {
			t.Errorf("DaemonMode = true, want false")
		}
		if opts.NoTUI {
			t.Errorf("NoTUI = true, want false")
		}
	}
	if opts.Verbose {
		t.Errorf("Verbose = true, want false")
	}
}

func TestParseFlags_Daemon(t *testing.T) {
	opts := ParseFlags([]string{"-d"})
	if !opts.DaemonMode {
		t.Errorf("DaemonMode = false, want true")
	}
	if opts.WSPort != "8765" {
		t.Errorf("WSPort = %q, want 8765", opts.WSPort)
	}
}

func TestParseFlags_Port(t *testing.T) {
	opts := ParseFlags([]string{"-p", "9999"})
	if opts.WSPort != "9999" {
		t.Errorf("WSPort = %q, want 9999", opts.WSPort)
	}
}

func TestParseFlags_VerboseEnv(t *testing.T) {
	t.Setenv("GBOT_VERBOSE", "1")
	opts := ParseFlags(nil)
	if !opts.Verbose {
		t.Errorf("Verbose = false, want true (GBOT_VERBOSE set)")
	}
}

func TestParseFlags_PortMissingValue(t *testing.T) {
	opts := ParseFlags([]string{"-p"})
	if opts.WSPort != "8765" {
		t.Errorf("WSPort = %q, want 8765 (default when -p has no value)", opts.WSPort)
	}
}

func TestParseFlags_AllFlags(t *testing.T) {
	opts := ParseFlags([]string{"-d", "-v", "-p", "3000"})
	if !opts.DaemonMode {
		t.Errorf("DaemonMode = false, want true")
	}
	if !opts.Verbose {
		t.Errorf("Verbose = false, want true")
	}
	if opts.WSPort != "3000" {
		t.Errorf("WSPort = %q, want 3000", opts.WSPort)
	}
}

func TestParseFlags_LongFlags(t *testing.T) {
	opts := ParseFlags([]string{"--daemon", "--verbose", "--port", "4000"})
	if !opts.DaemonMode {
		t.Errorf("DaemonMode = false, want true")
	}
	if !opts.Verbose {
		t.Errorf("Verbose = false, want true")
	}
	if opts.WSPort != "4000" {
		t.Errorf("WSPort = %q, want 4000", opts.WSPort)
	}
}

// TestParseFlags_PortImpliesNoTUI pins the 2026-09-23 flag matrix:
// bare `gbot` = TUI only (unchanged); ANY explicit -p/--port presence
// (value given or not, default value or not) = serve wui, no TUI.
// -d is NoTUI + the global isolated daemon (chdir).
func TestParseFlags_PortImpliesNoTUI(t *testing.T) {
	if runtime.GOOS == "android" {
		t.Skip("android forces DaemonMode; flag matrix is moot")
	}
	cases := []struct {
		name     string
		args     []string
		wantNoT  bool
		wantPT   string
		wantDaem bool
	}{
		{"no flags", nil, false, "8765", false},
		{"bare -p", []string{"-p"}, true, "8765", false},
		{"-p with port", []string{"-p", "1234"}, true, "1234", false},
		{"explicit default port still counts", []string{"-p", "8765"}, true, "8765", false},
		{"--port long form", []string{"--port", "9"}, true, "9", false},
		{"verbose alone stays TUI", []string{"-v"}, false, "8765", false},
		{"-p followed by a flag keeps default port", []string{"-p", "-v"}, true, "8765", false},
		{"daemon is NoTUI and isolated", []string{"-d"}, true, "8765", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := ParseFlags(tc.args)
			if opts.NoTUI != tc.wantNoT {
				t.Errorf("NoTUI = %v, want %v", opts.NoTUI, tc.wantNoT)
			}
			if opts.WSPort != tc.wantPT {
				t.Errorf("WSPort = %q, want %q", opts.WSPort, tc.wantPT)
			}
			if opts.DaemonMode != tc.wantDaem {
				t.Errorf("DaemonMode = %v, want %v", opts.DaemonMode, tc.wantDaem)
			}
		})
	}

	// `-p -v` must keep Verbose — the flag after -p is not its port.
	if opts := ParseFlags([]string{"-p", "-v"}); !opts.Verbose {
		t.Error("-v after bare -p must still register as Verbose")
	}
}
