package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/tool/task"
)

// ---------------------------------------------------------------------------
// Start gates — dream engine and session memory follow the config switches
// ---------------------------------------------------------------------------

func homeWithSettings(t *testing.T, extra string) string {
	t.Helper()
	pinSharedCaches(t)
	home := t.TempDir()
	settings := `{"providers":[{"name":"t","type":"openai","url":"http://127.0.0.1:1","keys":["k"],"models":{"m":{"max_tokens":"32k","context":"128k"}}}],"model":{"default":"t/m"}` + extra + `}`
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0o755); err != nil {
		t.Fatalf("mkdir .gbot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return home
}

func startForGates(t *testing.T, extra string) *Instance {
	t.Helper()
	t.Setenv("HOME", homeWithSettings(t, extra))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:0")
	t.Setenv("GBOT_SUPERVISED", "")
	startInTempProject(t)

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutdownInstance(t, inst)
	return inst
}

func TestStart_DreamEngineIsASystemEngineWithWhitelistedTools(t *testing.T) {
	inst := startForGates(t, "")

	vs := inst.EngineMgr.Get("dream")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("dream engine missing, manager holds %v", engineIDs(inst.EngineMgr))
	}
	if vs.ID != "dream" || vs.Name != "Dream" {
		t.Errorf("dream view state = (%q, %q), want (dream, Dream)", vs.ID, vs.Name)
	}
	if !vs.System {
		t.Error("the dream engine must be marked System")
	}
	if vs.Model != "t/m" {
		t.Errorf("dream Model = %q, want %q (provider name + model)", vs.Model, "t/m")
	}
	if vs.ActiveSessionID == "" || vs.ActiveSessionID != vs.Engine.SessionID() {
		t.Errorf("dream ActiveSessionID = %q, want the engine's session %q", vs.ActiveSessionID, vs.Engine.SessionID())
	}

	tools := vs.Engine.AllTools()
	for _, want := range []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob", "Recall"} {
		if _, ok := tools[want]; !ok {
			t.Errorf("dream tool set is missing the whitelisted tool %q (has %d tools)", want, len(tools))
		}
	}
	for _, unwanted := range []string{"Agent", "TaskCreate", "Repl"} {
		if _, ok := tools[unwanted]; ok {
			t.Errorf("dream tool set must not include %q (whitelist is Read/Write/Edit/Bash/Grep/Glob/Recall)", unwanted)
		}
	}

	// Positive control for the session-memory gate in the same boot.
	eng, _, err := inst.EngineFactory("eng-sn", "sn", "t", "m")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer eng.Close()
	if eng.SessionMemory() == nil {
		t.Error("session memory must be wired when session_notes is not disabled")
	}
}

func TestStart_DreamDisabledLeavesNoDreamEngine(t *testing.T) {
	inst := startForGates(t, `,"dream":{"disabled":true}`)

	if vs := inst.EngineMgr.Get("dream"); vs != nil {
		t.Errorf("dream engine exists despite dream.disabled=true: %+v", vs)
	}
	if inst.EngineMgr.Get("main") == nil {
		t.Errorf("main engine must survive, manager holds %v", engineIDs(inst.EngineMgr))
	}
}

func TestStart_SessionNotesDisabledLeavesNoSessionMemory(t *testing.T) {
	inst := startForGates(t, `,"session_notes":{"disabled":true}`)

	eng, _, err := inst.EngineFactory("eng-off", "off", "t", "m")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer eng.Close()
	if eng.SessionMemory() != nil {
		t.Error("session memory must NOT be wired when session_notes.disabled=true")
	}
}

// ---------------------------------------------------------------------------
// Cleanup — the nil guards must hold on a partially-initialized Instance
// ---------------------------------------------------------------------------

func TestCleanup_NilGuardsHoldOnPartialInstance(t *testing.T) {
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "no-engine", Engine: nil})

	deps := engine.SharedDeps{WorkingDir: t.TempDir()}
	refs := engine.CreateTools(deps, task.NewList(""))
	store, err := media.New()
	if err != nil {
		t.Fatalf("media.New: %v", err)
	}
	t.Cleanup(store.Close)

	var pidCleanups int
	inst := &Instance{
		MainRefs:    &refs,
		EngineMgr:   mgr,
		MediaStores: []*media.Store{nil, store},
		PIDCleanup:  func() { pidCleanups++ },
	}

	inst.Cleanup()

	if pidCleanups != 1 {
		t.Errorf("PIDCleanup ran %d times, want exactly 1", pidCleanups)
	}

	// MainRefs present but its REPL unset (a lazily-built ToolRefs): the
	// guard must keep Cleanup from dereferencing nil.
	emptyRefs := engine.ToolRefs{}
	(&Instance{MainRefs: &emptyRefs}).Cleanup()

	// No PIDCleanup closure: Cleanup must be a no-op, not a panic.
	(&Instance{}).Cleanup()
}

// ---------------------------------------------------------------------------
// Supervised stepdown — the re-claimed lock keeps the 0644 lock-file mode
// ---------------------------------------------------------------------------

func TestSupervisedStepdown_ReclaimKeepsLockFileMode(t *testing.T) {
	pidPath := resetStepdown(t)
	defer shortReclaimTTL(t)()

	// The release must actually delete the lock: supervisedStepdown's re-claim
	// is existence-guarded, so a no-op release leaves the original file in
	// place and the assertions below would read the file the test itself wrote.
	supervisedStepdown(idleReport, func() {
		if err := os.Remove(pidPath); err != nil {
			t.Errorf("release: remove PID file: %v", err)
		}
	}, filepath.Dir(pidPath))

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("the lock file must be gone before the TTL re-claim fires, stat err = %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond) // REAL-TIME: bounded poll for the 60ms TTL re-claim
	for time.Now().Before(deadline) {                  // REAL-TIME
		info, err := os.Stat(pidPath)
		if err == nil {
			if info.Mode().Perm() != 0o644 {
				t.Fatalf("re-claimed PID file mode = %v, want -rw-r--r--", info.Mode().Perm())
			}
			data, err := os.ReadFile(pidPath)
			if err != nil {
				t.Fatalf("read re-claimed PID file: %v", err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				t.Fatalf("re-claimed PID file contains %q, want a bare pid: %v", string(data), err)
			}
			if pid != os.Getpid() {
				t.Fatalf("re-claimed PID = %d, want our %d", pid, os.Getpid())
			}
			return
		}
		if !os.IsNotExist(err) {
			t.Fatalf("stat PID file: %v", err)
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: re-check loop for the TTL re-claim
	}
	t.Fatal("TTL re-claim never restored the PID file")
}

// ---------------------------------------------------------------------------
// Log file — created with the 0644 log mode
// ---------------------------------------------------------------------------

func TestSetupLogFile_CreatesWithLogMode(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gbot.log")

	f := setupLogFile(logPath)
	if f == nil {
		t.Fatal("setupLogFile returned nil for a writable path")
	}
	defer f.Close()

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("new log file mode = %v, want -rw-r--r--", info.Mode().Perm())
	}
}

// ---------------------------------------------------------------------------
// Flags — the port value must not swallow a following flag
// ---------------------------------------------------------------------------

func TestParseFlags_PortDoesNotConsumeFollowingFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"short then verbose", []string{"-p", "-v"}},
		{"short then long daemon", []string{"-p", "--daemon"}},
		{"long then short port flag", []string{"--port", "-p"}},
	} {
		opts := ParseFlags(tc.args)
		if opts.WSPort != "8765" {
			t.Errorf("%s: WSPort = %q, want the default 8765 (a leading dash is not a port)", tc.name, opts.WSPort)
		}
		if !opts.NoTUI {
			t.Errorf("%s: NoTUI = false, want true (any -p/--port presence serves wui)", tc.name)
		}
	}

	opts := ParseFlags([]string{"-p", "-v"})
	if !opts.Verbose {
		t.Error("-p -v: Verbose = false, want true (the -v must still be parsed)")
	}
	if opts.DaemonMode {
		t.Error("-p -v: DaemonMode = true, want false")
	}
}

func TestParseFlags_EmptyVerboseEnvStaysQuiet(t *testing.T) {
	t.Setenv("GBOT_VERBOSE", "")
	opts := ParseFlags(nil)
	if opts.Verbose {
		t.Error("Verbose = true, want false for an empty GBOT_VERBOSE")
	}
	if opts.WSPort != "8765" {
		t.Errorf("WSPort = %q, want 8765", opts.WSPort)
	}
	if opts.NoTUI || opts.DaemonMode {
		t.Errorf("bare invocation must stay TUI-attached, got %+v", opts)
	}
}
