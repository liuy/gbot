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
	} else {
		if opts.DaemonMode {
			t.Errorf("DaemonMode = true, want false")
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
