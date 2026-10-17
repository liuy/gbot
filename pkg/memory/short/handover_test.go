package short

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// This file ports the 2026-10-17 incident reproduction (E1 harness,
// /tmp/rca/harness) into the repo. The choreography replays the tableflip
// upgrade handover: the parent commits past the WAL autocheckpoint horizon
// and leaves an un-checkpointed tail, a child process opens its own Store on
// the same file while the parent is still alive, the parent hands over, and
// the child writes afterwards.
//
// Two always-on variants pin the fix and its complement:
//
//   - TestHandoverV0SerialReopenAllSurvive — serial handover (close before
//     reopen) loses nothing.
//   - TestHandoverUpgradeSkipCloseAllSurvive — the production fix shape:
//     the parent EXITS WITHOUT Close while the child is attached (crash
//     semantics, mirroring watchUpgradeExit's parent-side skip). The
//     committed -wal frames stay on disk and shared with the child, which
//     reads them and later checkpoints; nothing is lost.
//
// The overlap variant is the incident spectest: the parent DOES call Close
// under the attached child. Before persistWAL that close deleted the
// -wal/-shm sidecars and the child's later commits landed in the unlinked
// ghost wal while returning nil (the raw loss class); with persistWAL the
// parent's close keeps the sidecars and the spectest passes. It stays
// gated behind GBOT_E1_REPRO=1 — cross-process choreography is too
// wall-clock-heavy for every `make check` — and is runnable on demand:
//
//	GBOT_E1_REPRO=1 go test ./pkg/memory/short/ -run E1 -count=1 -v

const (
	e1PreRows   = 4  // e4-analog pre-window survivors
	e1BallastKB = 40 // matches incident tool-output sizes (38-39KB)
	e1TailRows  = 15 // e3/e4-analog tail: 4 + 11, all un-checkpointed
	e1ChildRows = 4  // child's first post-close writes

	// WAL autocheckpoint fires at 1000 frames; ballast to just under that so
	// the final ballast commit is the threshold-crossing checkpoint — the
	// incident's checkpoint horizon, after which the tail stays in the WAL.
	e1BallastTargetFrames = 990

	e1TailCadence       = 120 * time.Millisecond
	e1ChildWriteSelect  = 1 * time.Second // scaled from production's +5s on a 6-8s overlap
	e1OverlapReadEvery  = 300 * time.Millisecond
	e1OverlapReadIters  = 6
	e1SigWait           = 45 * time.Second
	e1BallastMaxCommits = 400
)

type e1Cfg struct {
	RunDir string `json:"run_dir"`
	DBPath string `json:"db_path"`
	S1     string `json:"s1"` // main-analog session
	S2     string `json:"s2"` // e3-analog session (also the child's)
	S3     string `json:"s3"` // e4-analog session
}

type e1ChildResult struct {
	CommitErrs []string `json:"commit_errs"`
	SelfCount  int      `json:"self_count"`
	SelfUUIDs  []string `json:"self_uuids"`
	// SidecarRead records the byte sizes of the raw db/-wal/-shm reads the
	// child performs at open (part of the proven-red harness choreography).
	SidecarRead string `json:"sidecar_read"`
	Err         string `json:"err,omitempty"`
}

type e1VerifyResult struct {
	S1Count int      `json:"s1_count"`
	S1UUIDs []string `json:"s1_uuids"`
	S2Count int      `json:"s2_count"`
	S2UUIDs []string `json:"s2_uuids"`
	S3Count int      `json:"s3_count"`
	S3UUIDs []string `json:"s3_uuids"`
	Err     string   `json:"err,omitempty"`
}

func e1Sig(runDir, name string) string { return filepath.Join(runDir, "sig", name) }

func e1Touch(runDir, name string) {
	if err := os.MkdirAll(filepath.Dir(e1Sig(runDir, name)), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(e1Sig(runDir, name), []byte("x"), 0o644); err != nil {
		panic(err)
	}
}

// e1WaitFor polls for a signal file from another REAL process; the
// choreography is wall-clock by construction.
func e1WaitFor(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)     // REAL-TIME: cross-process signal poll
	for time.Now().Before(deadline) { // REAL-TIME: cross-process signal poll
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: cross-process signal poll
	}
	return false
}

func e1FileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return st.Size()
}

func e1Content(marker string, kb int) string {
	pad := max(kb*1024-len(marker)-64, 0)
	return fmt.Sprintf(`[{"type":"text","text":%q}]`, marker+" "+strings.Repeat("x", pad))
}

func e1Msg(uuid string, kb int) *TranscriptMessage {
	return &TranscriptMessage{
		UUID:      uuid,
		Type:      "user",
		Content:   e1Content("MARK "+uuid, kb),
		CreatedAt: time.Now(), // REAL-TIME: persisted timestamp field

	}
}

// e1Spawn re-execs the test binary in a role. The child is reaped by the
// caller via cmd.Wait (goleak requires no lingering wait goroutines).
func e1Spawn(role, cfgPath, logPath string) (*exec.Cmd, *os.File, error) {
	logF, err := os.Create(logPath)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "GBOT_E1_ROLE="+role, "GBOT_E1_CFG="+cfgPath)
	cmd.Stdout = logF
	cmd.Stderr = logF
	if err := cmd.Start(); err != nil {
		if cerr := logF.Close(); cerr != nil {
			return nil, nil, fmt.Errorf("spawn %s: %w (log close: %v)", role, err, cerr)
		}
		return nil, nil, err
	}
	return cmd, logF, nil
}

// e1FailReap kills and reaps a role process on a fatal-path failure, with
// the exit status logged rather than discarded.
func e1FailReap(t *testing.T, cmd *exec.Cmd, logF *os.File, msg string) {
	t.Helper()
	if err := cmd.Process.Kill(); err != nil {
		t.Logf("%s: kill: %v", msg, err)
	}
	if err := cmd.Wait(); err != nil {
		t.Logf("%s: reap: %v", msg, err)
	}
	if logF != nil {
		if cerr := logF.Close(); cerr != nil {
			t.Logf("%s: log close: %v", msg, cerr)
		}
	}
	t.Fatal(msg)
}

func e1ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func e1WriteJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// e1ChildMain mirrors the production upgraded child: open the store, run the
// boot reads and the write-pool SELECT while the parent may still be alive,
// stay attached, and write only after the parent closed (production: the
// child's first write 13:26:29 > parent close ~13:25:44).
func e1ChildMain() int {
	var cfg e1Cfg
	if err := e1ReadJSON(os.Getenv("GBOT_E1_CFG"), &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "e1 child: read cfg: %v\n", err)
		return 2
	}
	res := &e1ChildResult{}
	fail := func(msg string) int {
		res.Err = msg
		if err := e1WriteJSON(filepath.Join(cfg.RunDir, "child_result.json"), res); err != nil {
			fmt.Fprintf(os.Stderr, "e1 child: write result: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "e1 child: %s\n", msg)
		return 1
	}

	st, err := NewStore(cfg.DBPath)
	if err != nil {
		return fail(fmt.Sprintf("NewStore: %v", err))
	}
	// Engine-restore reads at boot.
	for _, sid := range []string{cfg.S1, cfg.S2, cfg.S3} {
		if _, err := st.LoadMessages(sid); err != nil {
			return fail(fmt.Sprintf("restore read: %v", err))
		}
	}
	e1Touch(cfg.RunDir, "child_opened")
	// Raw sidecar reads at open (harness copySidecars analog, load-bearing
	// for the reproduction: without I/O over the sidecar files here the
	// parent's close skips its checkpoint+delete and no loss occurs).
	var sizes []string
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, rerr := os.ReadFile(cfg.DBPath + suffix)
		sizes = append(sizes, fmt.Sprintf("%s=%d", "memory.db"+suffix, len(b)))
		if rerr != nil {
			res.SidecarRead = rerr.Error()
		}
	}
	if res.SidecarRead == "" {
		res.SidecarRead = strings.Join(sizes, " ")
	}
	// The startSessionCleanup analog: a write-pool SELECT, +1s scaled from
	// production's +5s on a 6-8s overlap.
	time.Sleep(e1ChildWriteSelect) // REAL-TIME: scaled production overlap delay

	if _, err := st.CleanupOldSessions(30 * 24 * time.Hour); err != nil {
		return fail(fmt.Sprintf("cleanup select: %v", err))
	}
	// Parent engines keep reading through the overlap.
	for range e1OverlapReadIters {
		if _, err := os.Stat(e1Sig(cfg.RunDir, "parent_closed")); err == nil {
			break
		}
		if _, err := st.LoadMessages(cfg.S1); err != nil {
			return fail(fmt.Sprintf("overlap read: %v", err))
		}
		time.Sleep(e1OverlapReadEvery) // REAL-TIME: engine read cadence
	}

	if !e1WaitFor(e1Sig(cfg.RunDir, "parent_closed"), e1SigWait) {
		return fail("timeout waiting for parent_closed")
	}
	for i := range e1ChildRows {
		uuid := fmt.Sprintf("e1-child-%02d", i)
		if err := st.AppendMessages(cfg.S2, []*TranscriptMessage{e1Msg(uuid, 5)}); err != nil {
			res.CommitErrs = append(res.CommitErrs, fmt.Sprintf("%s: %v", uuid, err))
		}
		time.Sleep(150 * time.Millisecond) // REAL-TIME: per-write persist cadence
	}
	// Discovery read: the incident child saw its own rows (self-visible).
	msgs, err := st.LoadMessages(cfg.S2)
	if err != nil {
		return fail(fmt.Sprintf("self read: %v", err))
	}
	res.SelfCount = len(msgs)
	for _, m := range msgs {
		res.SelfUUIDs = append(res.SelfUUIDs, m.UUID)
	}
	if err := e1WriteJSON(filepath.Join(cfg.RunDir, "child_result.json"), res); err != nil {
		return fail(fmt.Sprintf("write result: %v", err))
	}
	// Stay open (a live daemon) until the parent's verifier is done.
	_ = e1WaitFor(e1Sig(cfg.RunDir, "child_may_exit"), e1SigWait)
	if err := st.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "e1 child: close: %v\n", err)
	}
	return 0
}

// e1ParentResult reports the parent role's choreography to the
// orchestrator: the sessions it created, every UUID set it committed, and
// its own final row counts right before exiting (the parent's belief that
// its tail is committed and self-visible).
type e1ParentResult struct {
	S1           string   `json:"s1"`
	S2           string   `json:"s2"`
	S3           string   `json:"s3"`
	PreUUIDs     []string `json:"pre_uuids"`
	BallastUUIDs []string `json:"ballast_uuids"`
	P2TailUUIDs  []string `json:"p2tail_uuids"`
	P3TailUUIDs  []string `json:"p3tail_uuids"`
	PrecloseS1   int      `json:"preclose_s1"`
	PrecloseS2   int      `json:"preclose_s2"`
	PrecloseS3   int      `json:"preclose_s3"`
	Err          string   `json:"err,omitempty"`
}

// e1ParentMain plays the upgraded parent under the skip-close handover: it
// commits the incident choreography (pre-window rows, ballast + crossing
// checkpoint, then an un-checkpointed tail), lets the child attach, keeps
// reading through the overlap, and EXITS WITHOUT Close — crash semantics,
// the production parent-side skip-close handover (see watchUpgradeExit).
func e1ParentMain() int {
	var cfg e1Cfg
	if err := e1ReadJSON(os.Getenv("GBOT_E1_CFG"), &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "e1 parent: read cfg: %v\n", err)
		return 2
	}
	res := &e1ParentResult{}
	fail := func(msg string) int {
		res.Err = msg
		if err := e1WriteJSON(filepath.Join(cfg.RunDir, "parent_result.json"), res); err != nil {
			fmt.Fprintf(os.Stderr, "e1 parent: write result: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "e1 parent: %s\n", msg)
		return 1
	}

	st, err := NewStore(cfg.DBPath)
	if err != nil {
		return fail(fmt.Sprintf("NewStore: %v", err))
	}
	s1, err := st.CreateSession(cfg.RunDir, "e1sk-main")
	if err != nil {
		return fail(fmt.Sprintf("CreateSession s1: %v", err))
	}
	s2, err := st.CreateSession(cfg.RunDir, "e1sk-e3")
	if err != nil {
		return fail(fmt.Sprintf("CreateSession s2: %v", err))
	}
	s3, err := st.CreateSession(cfg.RunDir, "e1sk-e4")
	if err != nil {
		return fail(fmt.Sprintf("CreateSession s3: %v", err))
	}
	res.S1, res.S2, res.S3 = s1.SessionID, s2.SessionID, s3.SessionID

	pre := e1UUIDs("pre", e1PreRows)
	if err := st.AppendMessages(s3.SessionID, e1Msgs(pre, 20)); err != nil {
		return fail(fmt.Sprintf("pre commit: %v", err))
	}
	res.PreUUIDs = pre
	ballast, err := e1BallastCommits(st, cfg.DBPath, s1.SessionID)
	if err != nil {
		return fail(err.Error())
	}
	res.BallastUUIDs = ballast
	time.Sleep(300 * time.Millisecond) // REAL-TIME: steady gap, harness steadyGap

	// The un-checkpointed tail: committed after the crossing checkpoint,
	// before the child attaches — exactly the incident's tail window.
	p2tail := e1UUIDs("p2tail", 4)
	p3tail := e1UUIDs("p3tail", 11)
	if err := st.AppendMessages(s2.SessionID, e1Msgs(p2tail, 20)); err != nil {
		return fail(fmt.Sprintf("p2tail commit: %v", err))
	}
	time.Sleep(e1TailCadence) // REAL-TIME: incident tail cadence
	if err := st.AppendMessages(s3.SessionID, e1Msgs(p3tail, 20)); err != nil {
		return fail(fmt.Sprintf("p3tail commit: %v", err))
	}
	res.P2TailUUIDs, res.P3TailUUIDs = p2tail, p3tail

	if err := e1WriteJSON(filepath.Join(cfg.RunDir, "parent_result.json"), res); err != nil {
		return fail(fmt.Sprintf("write result: %v", err))
	}
	e1Touch(cfg.RunDir, "parent_ready")

	// The child attaches while this parent stays live — the production
	// overlap the fix must survive.
	if !e1WaitFor(e1Sig(cfg.RunDir, "child_opened"), e1SigWait) {
		return fail("timeout waiting for child_opened")
	}
	// Parent engines keep reading through the overlap.
	for range e1OverlapReadIters {
		if _, err := st.LoadMessages(s1.SessionID); err != nil {
			return fail(fmt.Sprintf("overlap read: %v", err))
		}
		time.Sleep(e1OverlapReadEvery) // REAL-TIME: engine read cadence
	}
	// The parent's own last word: its tail is committed and self-visible.
	for _, x := range []struct {
		sid   string
		count *int
	}{
		{s1.SessionID, &res.PrecloseS1},
		{s2.SessionID, &res.PrecloseS2},
		{s3.SessionID, &res.PrecloseS3},
	} {
		msgs, err := st.LoadMessages(x.sid)
		if err != nil {
			return fail(fmt.Sprintf("preclose read: %v", err))
		}
		*x.count = len(msgs)
	}
	if err := e1WriteJSON(filepath.Join(cfg.RunDir, "parent_result.json"), res); err != nil {
		return fail(fmt.Sprintf("rewrite result: %v", err))
	}

	// The skip itself: exit WITHOUT Close. Store.Close here would run the
	// final checkpoint and delete the -wal/-shm sidecars out from under the
	// attached child — its later commits land in the unlinked ghost wal
	// and vanish (the 2026-10-17 mechanism). Skipping Close leaves the
	// committed tail durable in the on-disk -wal for the child.
	os.Exit(0)
	return 0 // unreachable: os.Exit above is terminal
}

// e1VerifyMain is the fresh third process: its counts are ground truth for
// what actually survived the handover.
func e1VerifyMain() int {
	var cfg e1Cfg
	if err := e1ReadJSON(os.Getenv("GBOT_E1_CFG"), &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "e1 verify: read cfg: %v\n", err)
		return 2
	}
	res := &e1VerifyResult{}
	fail := func(msg string) int {
		res.Err = msg
		if err := e1WriteJSON(filepath.Join(cfg.RunDir, "verify_result.json"), res); err != nil {
			fmt.Fprintf(os.Stderr, "e1 verify: write result: %v\n", err)
		}
		return 1
	}
	st, err := NewStore(cfg.DBPath)
	if err != nil {
		return fail(fmt.Sprintf("NewStore: %v", err))
	}
	for _, x := range []struct {
		sid   string
		count *int
		uuids *[]string
	}{
		{cfg.S1, &res.S1Count, &res.S1UUIDs},
		{cfg.S2, &res.S2Count, &res.S2UUIDs},
		{cfg.S3, &res.S3Count, &res.S3UUIDs},
	} {
		msgs, err := st.LoadMessages(x.sid)
		if err != nil {
			return fail(fmt.Sprintf("load: %v", err))
		}
		*x.count = len(msgs)
		for _, m := range msgs {
			*x.uuids = append(*x.uuids, m.UUID)
		}
	}
	if err := st.Close(); err != nil {
		return fail(fmt.Sprintf("close: %v", err))
	}
	if err := e1WriteJSON(filepath.Join(cfg.RunDir, "verify_result.json"), res); err != nil {
		return fail(fmt.Sprintf("write result: %v", err))
	}
	return 0
}

func e1AssertDurability(t *testing.T, what string, gotCount, wantCount int, gotUUIDs, wantUUIDs []string) {
	t.Helper()
	if gotCount != wantCount {
		t.Errorf("%s: count=%d, want %d (committed rows must survive a fresh open)", what, gotCount, wantCount)
	}
	sort.Strings(gotUUIDs)
	sort.Strings(wantUUIDs)
	if strings.Join(gotUUIDs, ",") != strings.Join(wantUUIDs, ",") {
		t.Errorf("%s: uuid set mismatch\ngot:  %v\nwant: %v", what, gotUUIDs, wantUUIDs)
	}
}

func e1UUIDs(tag string, n int) []string {
	var out []string
	for i := range n {
		out = append(out, fmt.Sprintf("e1-%s-%02d", tag, i))
	}
	return out
}

// e1Ballast commits single rows until the WAL sits just under the
// autocheckpoint threshold, then one crossing commit pushes past it — the
// autocheckpoint backfills everything through that row (the incident's
// checkpoint horizon). Rows committed after this point stay in the WAL tail.
// Returns the committed UUIDs. Error-returning so role processes (no
// *testing.T) can run the same choreography.
func e1BallastCommits(st *Store, dbPath, sid string) ([]string, error) {
	walPath := dbPath + "-wal"
	pageSize := uint32(4096)
	if b, err := os.ReadFile(walPath); err == nil && len(b) >= 12 {
		pageSize = binary.BigEndian.Uint32(b[8:12])
	}
	target := int64(32 + e1BallastTargetFrames*(int(pageSize)+24))
	var uuids []string
	i := 0
	for ; i < e1BallastMaxCommits && e1FileSize(walPath) < target; i++ {
		uuid := fmt.Sprintf("e1-ballast-%03d", i)
		if err := st.AppendMessages(sid, []*TranscriptMessage{e1Msg(uuid, e1BallastKB)}); err != nil {
			return nil, fmt.Errorf("ballast commit %d: %w", i, err)
		}
		uuids = append(uuids, uuid)
	}
	if i == e1BallastMaxCommits {
		return nil, fmt.Errorf("ballast never reached %d bytes (page size %d)", target, pageSize)
	}
	cross := "e1-cross-000"
	if err := st.AppendMessages(sid, []*TranscriptMessage{e1Msg(cross, e1BallastKB)}); err != nil {
		return nil, fmt.Errorf("crossing commit: %w", err)
	}
	uuids = append(uuids, cross)
	return uuids, nil
}

func e1Ballast(t *testing.T, st *Store, dbPath, sid string) []string {
	t.Helper()
	uuids, err := e1BallastCommits(st, dbPath, sid)
	if err != nil {
		t.Fatal(err)
	}
	return uuids
}

// TestHandoverV0SerialReopenAllSurvive: the serial (fixed-order) handover —
// the parent fully closes before anyone reopens. Everything must survive.
func TestHandoverV0SerialReopenAllSurvive(t *testing.T) {
	runDir := t.TempDir()
	dbPath := filepath.Join(runDir, "memory.db")
	if err := os.MkdirAll(filepath.Join(runDir, "sig"), 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s1, err := st.CreateSession(runDir, "e1-v0")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s2, err := st.CreateSession(runDir, "e1-v0-child")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	tail := e1UUIDs("tail", e1TailRows)
	child := e1UUIDs("child", e1ChildRows)
	if err := st.AppendMessages(s1.SessionID, e1Msgs(tail, 20)); err != nil {
		t.Fatalf("tail commit: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("parent close: %v", err)
	}
	// The post-close writer replays the child's role on the serial file.
	st2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen NewStore: %v", err)
	}
	if err := st2.AppendMessages(s2.SessionID, e1Msgs(child, 5)); err != nil {
		t.Fatalf("child commit: %v", err)
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("child close: %v", err)
	}

	cfgPath := filepath.Join(runDir, "cfg.json")
	if err := e1WriteJSON(cfgPath, e1Cfg{RunDir: runDir, DBPath: dbPath, S1: s1.SessionID, S2: s2.SessionID, S3: s2.SessionID}); err != nil {
		t.Fatal(err)
	}
	var vr e1VerifyResult
	e1RunVerifier(t, runDir, cfgPath, &vr)
	e1AssertDurability(t, "V0 S1 (parent)", vr.S1Count, e1TailRows, vr.S1UUIDs, tail)
	e1AssertDurability(t, "V0 S2 (child)", vr.S2Count, e1ChildRows, vr.S2UUIDs, child)
}

// TestHandoverUpgradeSkipCloseAllSurvive: the fix-shaped handover — the
// parent role commits the incident choreography (checkpointed ballast plus
// an un-checkpointed tail), the child attaches while the parent is live,
// and the parent EXITS WITHOUT Close (the production parent-side skip;
// see watchUpgradeExit). Everything — the parent's un-checkpointed tail
// AND the child's post-exit writes — must survive a fresh third open, both
// while the child is still attached and after its final Close. Always on:
// this pins the store-level contract that makes the parent-side skip safe
// (committed -wal frames are shared with an attached successor via the
// wal-index; historically a Close under it also deleted the sidecars —
// now prevented by persistWAL).
func TestHandoverUpgradeSkipCloseAllSurvive(t *testing.T) {
	runDir := t.TempDir()
	dbPath := filepath.Join(runDir, "memory.db")
	if err := os.MkdirAll(filepath.Join(runDir, "sig"), 0o755); err != nil {
		t.Fatal(err)
	}

	parentCfg := filepath.Join(runDir, "pcfg.json")
	if err := e1WriteJSON(parentCfg, e1Cfg{RunDir: runDir, DBPath: dbPath}); err != nil {
		t.Fatal(err)
	}
	parentCmd, parentLog, err := e1Spawn("parent", parentCfg, filepath.Join(runDir, "parent.log"))
	if err != nil {
		t.Fatalf("spawn parent: %v", err)
	}
	parentRes := filepath.Join(runDir, "parent_result.json")
	if !e1WaitFor(parentRes, e1SigWait) {
		e1FailReap(t, parentCmd, parentLog, "parent never produced a result")
	}
	var pr e1ParentResult
	if err := e1ReadJSON(parentRes, &pr); err != nil {
		e1FailReap(t, parentCmd, parentLog, "read parent result: "+err.Error())
	}
	if pr.Err != "" {
		if err := parentCmd.Wait(); err != nil {
			t.Logf("parent reap: %v", err)
		}
		if cerr := parentLog.Close(); cerr != nil {
			t.Logf("parent log close: %v", cerr)
		}
		t.Fatalf("parent role failed: %s", pr.Err)
	}

	cfgPath := filepath.Join(runDir, "cfg.json")
	if err := e1WriteJSON(cfgPath, e1Cfg{RunDir: runDir, DBPath: dbPath, S1: pr.S1, S2: pr.S2, S3: pr.S3}); err != nil {
		e1FailReap(t, parentCmd, parentLog, "write child cfg: "+err.Error())
	}
	childCmd, childLog, err := e1Spawn("child", cfgPath, filepath.Join(runDir, "child.log"))
	if err != nil {
		e1FailReap(t, parentCmd, parentLog, fmt.Sprintf("spawn child: %v", err))
	}

	// The parent sees child_opened, reads through the overlap, records its
	// final counts, and exits WITHOUT Close.
	if err := parentCmd.Wait(); err != nil {
		e1FailReap(t, childCmd, childLog, fmt.Sprintf("parent role exit: %v", err))
	}
	if cerr := parentLog.Close(); cerr != nil {
		t.Errorf("parent log close: %v", cerr)
	}
	if err := e1ReadJSON(parentRes, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.Err != "" {
		t.Fatalf("parent role failed late: %s", pr.Err)
	}
	// The parent's own pre-exit belief: every commit visible to itself.
	if pr.PrecloseS1 != len(pr.BallastUUIDs) || pr.PrecloseS2 != len(pr.P2TailUUIDs) || pr.PrecloseS3 != len(pr.PreUUIDs)+len(pr.P3TailUUIDs) {
		t.Errorf("parent preclose counts = %d/%d/%d, want %d/%d/%d",
			pr.PrecloseS1, pr.PrecloseS2, pr.PrecloseS3,
			len(pr.BallastUUIDs), len(pr.P2TailUUIDs), len(pr.PreUUIDs)+len(pr.P3TailUUIDs))
	}
	// Mechanism pin: the skip-close exit must leave the sidecars on disk —
	// a parent Close would have deleted them under the attached child.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err != nil {
			t.Errorf("memory.db%s missing after parent exit without close — the attached child needs it: %v", suffix, err)
		}
	}
	e1Touch(runDir, "parent_closed")

	var cr e1ChildResult
	if !e1WaitFor(filepath.Join(runDir, "child_result.json"), e1SigWait) {
		e1FailReap(t, childCmd, childLog, "child never produced a result")
	}
	if err := e1ReadJSON(filepath.Join(runDir, "child_result.json"), &cr); err != nil {
		e1FailReap(t, childCmd, childLog, "read child result: "+err.Error())
	}
	if cr.Err != "" {
		e1FailReap(t, childCmd, childLog, "child failed: "+cr.Err)
	}
	if len(cr.CommitErrs) != 0 {
		e1FailReap(t, childCmd, childLog, fmt.Sprintf("child commits returned errors: %v", cr.CommitErrs))
	}
	// The attached child must SEE the parent's un-checkpointed tail (shared
	// wal-index) alongside its own post-exit rows.
	childWant := e1UUIDs("child", e1ChildRows)
	selfWant := append(append([]string{}, pr.P2TailUUIDs...), childWant...)
	e1AssertDurability(t, "child self-view (parent tail + own rows)", cr.SelfCount, len(selfWant), cr.SelfUUIDs, selfWant)

	// Fresh third open, child still attached: everything survives.
	var vr e1VerifyResult
	e1RunVerifier(t, runDir, cfgPath, &vr)
	e1AssertDurability(t, "skipclose S1 (ballast, checkpointed)", vr.S1Count, len(pr.BallastUUIDs), vr.S1UUIDs, pr.BallastUUIDs)
	e1AssertDurability(t, "skipclose S2 (parent tail + child post-exit)", vr.S2Count, len(pr.P2TailUUIDs)+e1ChildRows, vr.S2UUIDs, append(append([]string{}, pr.P2TailUUIDs...), childWant...))
	e1AssertDurability(t, "skipclose S3 (pre + parent tail)", vr.S3Count, len(pr.PreUUIDs)+len(pr.P3TailUUIDs), vr.S3UUIDs, append(append([]string{}, pr.PreUUIDs...), pr.P3TailUUIDs...))

	// Let the child exit; its own Close is now the last connection — the
	// legitimate final checkpoint. The on-disk state must hold everything.
	e1Touch(runDir, "child_may_exit")
	if err := childCmd.Wait(); err != nil {
		t.Errorf("child exit: %v", err)
	}
	if err := childLog.Close(); err != nil {
		t.Errorf("child log close: %v", err)
	}
	e1RunVerifier(t, runDir, cfgPath, &vr)
	e1AssertDurability(t, "skipclose post-child-close S1", vr.S1Count, len(pr.BallastUUIDs), vr.S1UUIDs, pr.BallastUUIDs)
	e1AssertDurability(t, "skipclose post-child-close S2", vr.S2Count, len(pr.P2TailUUIDs)+e1ChildRows, vr.S2UUIDs, append(append([]string{}, pr.P2TailUUIDs...), childWant...))
	e1AssertDurability(t, "skipclose post-child-close S3", vr.S3Count, len(pr.PreUUIDs)+len(pr.P3TailUUIDs), vr.S3UUIDs, append(append([]string{}, pr.PreUUIDs...), pr.P3TailUUIDs...))
}

// TestE1OverlapHandoverCommittedRowsSurvive: the production-incident
// choreography — the child's Store opens while the parent is still alive,
// the parent commits its tail and closes, the child writes post-close.
// Green under persistWAL: the parent's Close keeps the -wal/-shm sidecars
// under the attached child, so the child's nil-returning commits land in
// the linked wal and survive every later open. Run on demand with
// GBOT_E1_REPRO=1 (see the file comment).
func TestE1OverlapHandoverCommittedRowsSurvive(t *testing.T) {
	if os.Getenv("GBOT_E1_REPRO") != "1" {
		t.Skip("incident spectest, green under persist-WAL; cross-process choreography run on demand: GBOT_E1_REPRO=1")
	}
	runDir := t.TempDir()
	dbPath := filepath.Join(runDir, "memory.db")
	if err := os.MkdirAll(filepath.Join(runDir, "sig"), 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s1, err := st.CreateSession(runDir, "e1-main")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s2, err := st.CreateSession(runDir, "e1-e3")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s3, err := st.CreateSession(runDir, "e1-e4")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// e4-analog pre-window survivors, then the ballast + crossing commit
	// that sets the checkpoint horizon.
	pre := e1UUIDs("pre", e1PreRows)
	if err := st.AppendMessages(s3.SessionID, e1Msgs(pre, 20)); err != nil {
		t.Fatalf("pre commit: %v", err)
	}
	ballast := e1Ballast(t, st, dbPath, s1.SessionID)
	time.Sleep(300 * time.Millisecond) // REAL-TIME: steady gap, harness steadyGap

	// e3/e4-analog tail: committed after the crossing checkpoint, before the
	// child spawns — exactly the incident's un-checkpointed tail window
	// (13:22:00–13:25:24 vs the 13:20:16 checkpoint horizon). Batched like
	// the harness: 4 on the e3-analog, 11 on the e4-analog.
	p2tail := e1UUIDs("p2tail", 4)
	p3tail := e1UUIDs("p3tail", 11)
	if err := st.AppendMessages(s2.SessionID, e1Msgs(p2tail, 20)); err != nil {
		t.Fatalf("p2tail commit: %v (incident requires all-nil)", err)
	}
	time.Sleep(e1TailCadence) // REAL-TIME: incident tail cadence
	if err := st.AppendMessages(s3.SessionID, e1Msgs(p3tail, 20)); err != nil {
		t.Fatalf("p3tail commit: %v (incident requires all-nil)", err)
	}
	time.Sleep(e1TailCadence) // REAL-TIME: incident tail cadence

	cfgPath := filepath.Join(runDir, "cfg.json")
	if err := e1WriteJSON(cfgPath, e1Cfg{RunDir: runDir, DBPath: dbPath, S1: s1.SessionID, S2: s2.SessionID, S3: s3.SessionID}); err != nil {
		t.Fatal(err)
	}

	// Child boots on the same file while this parent Store stays open.
	childCmd, childLog, err := e1Spawn("child", cfgPath, filepath.Join(runDir, "child.log"))
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}
	if !e1WaitFor(e1Sig(runDir, "child_opened"), e1SigWait) {
		e1FailReap(t, childCmd, childLog, "child never signaled opened")
	}

	// Parent engines keep reading through the overlap (harness: 6x300ms).
	for range 6 {
		if _, err := st.LoadMessages(s1.SessionID); err != nil {
			t.Fatalf("overlap read: %v", err)
		}
		time.Sleep(e1OverlapReadEvery) // REAL-TIME: engine read cadence
	}

	// Preclose reads (harness: parent's belief right before close).
	for _, sid := range []string{s1.SessionID, s2.SessionID, s3.SessionID} {
		if _, err := st.LoadMessages(sid); err != nil {
			t.Fatalf("preclose read: %v", err)
		}
	}

	if err := st.Close(); err != nil {
		t.Fatalf("parent close: %v", err)
	}
	e1Touch(runDir, "parent_closed")
	// Incident signature diagnostics: the destructive step is the parent's
	// close deleting -wal/-shm under the still-attached child (ghost fds).
	for _, suf := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suf); err == nil {
			t.Logf("post-close: memory.db%s exists", suf)
		} else {
			t.Logf("post-close: memory.db%s GONE (deleted by parent close)", suf)
		}
	}
	if entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", childCmd.Process.Pid)); err == nil {
		for _, ent := range entries {
			if link, lerr := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", childCmd.Process.Pid, ent.Name())); lerr == nil && strings.Contains(link, "memory.db") {
				t.Logf("child fd -> %s", link)
			}
		}
	}

	var cr e1ChildResult
	if !e1WaitFor(filepath.Join(runDir, "child_result.json"), e1SigWait) {
		e1FailReap(t, childCmd, childLog, "child never produced a result")
	}
	if err := e1ReadJSON(filepath.Join(runDir, "child_result.json"), &cr); err != nil {
		t.Fatal(err)
	}
	if cr.Err != "" {
		t.Fatalf("child failed: %s", cr.Err)
	}
	if len(cr.CommitErrs) != 0 {
		t.Fatalf("child commits returned errors (incident requires all-nil): %v", cr.CommitErrs)
	}
	// Incident signature: the child's own reads see its rows (self-visible)
	// alongside everything committed before the handover.
	childWant := e1UUIDs("child", e1ChildRows)
	selfWant := append(append([]string{}, p2tail...), childWant...)
	e1AssertDurability(t, "child self-view", cr.SelfCount, len(selfWant), cr.SelfUUIDs, selfWant)

	var vr e1VerifyResult
	e1RunVerifier(t, runDir, cfgPath, &vr)
	// Under persistWAL every row survives: the parent's rows via the
	// close-time checkpoint backfill, the child's post-close rows because
	// their wal stays linked under them.
	e1AssertDurability(t, "E1 S1 (ballast, checkpointed)", vr.S1Count, len(ballast), vr.S1UUIDs, ballast)
	e1AssertDurability(t, "E1 S2 (e3 tail + child victims)", vr.S2Count, len(p2tail)+e1ChildRows, vr.S2UUIDs, append(append([]string{}, p2tail...), childWant...))
	e1AssertDurability(t, "E1 S3 (e4 pre+tail)", vr.S3Count, len(pre)+len(p3tail), vr.S3UUIDs, append(append([]string{}, pre...), p3tail...))

	e1Touch(runDir, "child_may_exit")
	if err := childCmd.Wait(); err != nil {
		t.Errorf("child exit: %v", err)
	}
	if err := childLog.Close(); err != nil {
		t.Errorf("child log close: %v", err)
	}
}

func e1RunVerifier(t *testing.T, runDir, cfgPath string, out *e1VerifyResult) {
	t.Helper()
	resPath := filepath.Join(runDir, "verify_result.json")
	// A previous verifier run's result must not satisfy this run's wait.
	if err := os.Remove(resPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove stale verify result: %v", err)
	}
	cmd, logF, err := e1Spawn("verify", cfgPath, filepath.Join(runDir, "verify.log"))
	if err != nil {
		t.Fatalf("spawn verifier: %v", err)
	}
	defer logF.Close()
	if !e1WaitFor(resPath, e1SigWait) {
		if err := cmd.Process.Kill(); err != nil {
			t.Logf("verifier kill: %v", err)
		}
		if err := cmd.Wait(); err != nil {
			t.Logf("verifier reap: %v", err)
		}
		t.Fatal("verifier never produced a result")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("verifier exit: %v", err)
	}
	if err := e1ReadJSON(resPath, out); err != nil {
		t.Fatal(err)
	}
	if out.Err != "" {
		t.Fatalf("verifier failed: %s", out.Err)
	}
}

func e1Msgs(uuids []string, kb int) []*TranscriptMessage {
	var out []*TranscriptMessage
	for _, u := range uuids {
		out = append(out, e1Msg(u, kb))
	}
	return out
}
