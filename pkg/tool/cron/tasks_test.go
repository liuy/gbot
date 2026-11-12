package cron

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := storeFor(dir)
	created := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	if err := s.Add(Task{ID: "aa11bb22", Cron: "*/5 * * * *", Prompt: "drink water", CreatedAt: created.UnixMilli(), Recurring: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Durable=false never reaches the file (CC strips the runtime flag), but a
	// zero-value Add is still persisted as given — the flag lives at the
	// Manager layer.
	tasks := s.Read()
	if len(tasks) != 1 {
		t.Fatalf("Read = %d tasks, want 1", len(tasks))
	}
	got := tasks[0]
	if got.ID != "aa11bb22" || got.Cron != "*/5 * * * *" || got.Prompt != "drink water" {
		t.Errorf("round-trip task = %+v", got)
	}
	if got.CreatedAt != created.UnixMilli() {
		t.Errorf("CreatedAt = %d, want %d", got.CreatedAt, created.UnixMilli())
	}
	if !got.Recurring {
		t.Error("Recurring = false, want true")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "scheduled_tasks.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(raw), `"tasks"`) {
		t.Errorf("file bytes lack tasks key: %s", raw)
	}
	if strings.Contains(string(raw), "durable") {
		t.Errorf("file bytes contain durable (must be stripped): %s", raw)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("file must end with a trailing newline")
	}
}

func TestStoreEmptyRemoveAllWritesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	s := storeFor(dir)
	if err := s.Add(Task{ID: "aa11bb22", Cron: "0 9 * * *", Prompt: "x", CreatedAt: 1000}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Remove([]string{"aa11bb22"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "scheduled_tasks.json"))
	if err != nil {
		t.Fatalf("read file after remove-all: %v", err)
	}
	var file struct {
		Tasks []Task `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("empty file is not valid JSON: %v (%s)", err, raw)
	}
	if len(file.Tasks) != 0 {
		t.Errorf("tasks = %d, want 0 (empty file, not deleted)", len(file.Tasks))
	}
	if !strings.Contains(string(raw), `"tasks": []`) {
		t.Errorf("empty file bytes = %s, want `\"tasks\": []`", raw)
	}
	if s.HasTasks() {
		t.Error("HasTasks after remove-all = true, want false")
	}
}

func TestStoreMalformedRowsDropped(t *testing.T) {
	dir := t.TempDir()
	body := `{"tasks":[
		{"id": 123, "cron": "0 9 * * *", "prompt": "x", "createdAt": 1000},
		{"id": "missing-created", "cron": "0 9 * * *", "prompt": "x"},
		{"id": "bad-cron", "cron": "not a cron", "prompt": "x", "createdAt": 1000},
		{"id": "valid1", "cron": "0 9 * * *", "prompt": "ok", "createdAt": 1700000000000, "recurring": true, "lastFiredAt": 1700000600000},
		null
	]}`
	if err := os.WriteFile(filepath.Join(dir, "scheduled_tasks.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	s := storeFor(dir)
	tasks := s.Read()
	if len(tasks) != 1 {
		t.Fatalf("Read = %d tasks, want 1 (only valid1 survives)", len(tasks))
	}
	if tasks[0].ID != "valid1" || !tasks[0].Recurring || tasks[0].LastFiredAt != 1700000600000 {
		t.Errorf("valid row = %+v", tasks[0])
	}
}

func TestStoreWholeFileGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scheduled_tasks.json"), []byte("}}} not json {{{"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	s := storeFor(dir)
	if tasks := s.Read(); len(tasks) != 0 {
		t.Errorf("Read on garbage file = %d tasks, want 0", len(tasks))
	}
	if s.HasTasks() {
		t.Error("HasTasks on garbage = true, want false")
	}
}

func TestStoreMissingFile(t *testing.T) {
	dir := t.TempDir()
	s := storeFor(dir)
	if tasks := s.Read(); len(tasks) != 0 {
		t.Errorf("Read on missing file = %d tasks, want 0", len(tasks))
	}
	if s.HasTasks() {
		t.Error("HasTasks on missing = true, want false")
	}
}

func TestStoreRemoveAndMarkFired(t *testing.T) {
	dir := t.TempDir()
	s := storeFor(dir)
	for _, id := range []string{"id1", "id2"} {
		if err := s.Add(Task{ID: id, Cron: "0 9 * * *", Prompt: "p", CreatedAt: 1000, Recurring: true}); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}
	// Absent ids are a no-op (another session raced us).
	if err := s.Remove([]string{"nope"}); err != nil {
		t.Fatalf("Remove absent: %v", err)
	}
	if tasks := s.Read(); len(tasks) != 2 {
		t.Fatalf("Remove absent changed task count: %d, want 2", len(tasks))
	}
	if err := s.Remove([]string{"id1"}); err != nil {
		t.Fatalf("Remove id1: %v", err)
	}
	tasks := s.Read()
	if len(tasks) != 1 || tasks[0].ID != "id2" {
		t.Fatalf("after Remove: %+v, want only id2", tasks)
	}

	// MarkFired stamps only matching ids; deleted ids are a no-op.
	if err := s.MarkFired([]string{"id2", "deleted"}, 1700000600000); err != nil {
		t.Fatalf("MarkFired: %v", err)
	}
	tasks = s.Read()
	if len(tasks) != 1 {
		t.Fatalf("MarkFired changed count: %d, want 1", len(tasks))
	}
	if tasks[0].LastFiredAt != 1700000600000 {
		t.Errorf("LastFiredAt = %d, want 1700000600000", tasks[0].LastFiredAt)
	}
	// No-match MarkFired leaves the file alone.
	if err := s.MarkFired([]string{"gone"}, 1); err != nil {
		t.Fatalf("MarkFired no-match: %v", err)
	}
	if tasks = s.Read(); len(tasks) != 1 || tasks[0].LastFiredAt != 1700000600000 {
		t.Errorf("no-match MarkFired mutated store: %+v", tasks)
	}
}

func TestStoreProcessSharedAndExternalEdit(t *testing.T) {
	dir := t.TempDir()
	a := storeFor(dir)
	b := storeFor(dir)
	if a != b {
		t.Fatal("storeFor must return the same Store for the same dir")
	}
	if err := a.Add(Task{ID: "ext1", Cron: "0 9 * * *", Prompt: "p", CreatedAt: 1000}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// A same-dir Store picked up later (different call, same process) sees it.
	if tasks := storeFor(dir).Read(); len(tasks) != 1 || tasks[0].ID != "ext1" {
		t.Fatalf("second store view = %+v", tasks)
	}
	// An external process overwrites the file; the next Read re-stats and
	// reloads (mtime/size change detection replaces CC's chokidar watcher).
	external := `{"tasks":[{"id":"ext2","cron":"*/7 * * * *","prompt":"from outside","createdAt":2000}]}`
	if err := os.WriteFile(filepath.Join(dir, "scheduled_tasks.json"), []byte(external), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	tasks := b.Read()
	if len(tasks) != 1 || tasks[0].ID != "ext2" || tasks[0].Prompt != "from outside" {
		t.Fatalf("external edit not seen: %+v", tasks)
	}
	// Distinct dirs get distinct stores.
	other := storeFor(t.TempDir())
	if other == a {
		t.Fatal("distinct dirs must not share a Store")
	}
}

func TestFindMissedTasks(t *testing.T) {
	pinLocalCST(t)
	nowMs := time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local).UnixMilli()
	tasks := []Task{
		// Created 7h ago (05:00) — today's 06:00 fire is strictly after
		// createdAt and already past → missed.
		{ID: "overdue", Cron: "0 6 * * *", Prompt: "p", CreatedAt: nowMs - 7*3600*1000},
		{ID: "future", Cron: "0 23 * * *", Prompt: "p", CreatedAt: nowMs - 60_000}, // next fire tonight
		{ID: "badcron", Cron: "0 0 31 2 *", Prompt: "p", CreatedAt: nowMs - 24*3600*1000},
		{ID: "recurring", Cron: "0 6 * * *", Prompt: "p", CreatedAt: nowMs - 7*3600*1000, Recurring: true},
	}
	missed := FindMissedTasks(tasks, nowMs)
	if len(missed) != 2 {
		t.Fatalf("FindMissedTasks = %d tasks, want 2 (overdue + recurring)", len(missed))
	}
	ids := map[string]bool{}
	for _, m := range missed {
		ids[m.ID] = true
	}
	if !ids["overdue"] || !ids["recurring"] {
		t.Errorf("missed ids = %v, want overdue+recurring", ids)
	}
}

func TestJitterDeterminism(t *testing.T) {
	pinLocalCST(t)
	from := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local).UnixMilli()
	first, ok1 := JitteredNextCronRunMs("*/5 * * * *", from, "ab12cd34")
	second, ok2 := JitteredNextCronRunMs("*/5 * * * *", from, "ab12cd34")
	if !ok1 || !ok2 {
		t.Fatalf("JitteredNextCronRunMs ok = %v/%v, want true", ok1, ok2)
	}
	if first != second {
		t.Errorf("jitter not deterministic: %d vs %d", first, second)
	}
	os1, ok1 := OneShotJitteredNextCronRunMs("*/5 * * * *", from, "ab12cd34")
	os2, ok2 := OneShotJitteredNextCronRunMs("*/5 * * * *", from, "ab12cd34")
	if !ok1 || !ok2 || os1 != os2 {
		t.Errorf("one-shot jitter not deterministic: %d/%v vs %d/%v", os1, ok1, os2, ok2)
	}
	// Invalid cron → !ok.
	if _, ok := JitteredNextCronRunMs("bad", from, "ab12cd34"); ok {
		t.Error("JitteredNextCronRunMs(bad) ok, want !ok")
	}
	if _, ok := OneShotJitteredNextCronRunMs("bad", from, "ab12cd34"); ok {
		t.Error("OneShotJitteredNextCronRunMs(bad) ok, want !ok")
	}
}

func TestRecurringJitterBounds(t *testing.T) {
	pinLocalCST(t)
	from := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local).UnixMilli()
	const cron = "*/5 * * * *"
	t1, ok := NextCronRunMs(cron, from)
	if !ok {
		t.Fatal("NextCronRunMs failed")
	}
	t2, ok := NextCronRunMs(cron, t1)
	if !ok {
		t.Fatal("NextCronRunMs(t1) failed")
	}
	for _, id := range []string{"00000001", "ab12cd34", "ffffffff"} {
		got, ok := JitteredNextCronRunMs(cron, from, id)
		if !ok {
			t.Fatalf("JitteredNextCronRunMs(%s) !ok", id)
		}
		maxJitter := float64(t2-t1) * recurringFrac
		if recurringCapMs < maxJitter {
			maxJitter = recurringCapMs
		}
		// Truncation to int64 ms: near-zero fracs yield sub-ms jitter → t1
		// itself (CC's float Date keeps the same millisecond).
		if got < t1 || got > t1+int64(maxJitter)+1 {
			t.Errorf("id %s: jittered = %d, want in [t1, t1+min(frac*gap,cap)] (t1=%d, max=%d)", id, got, t1, t1+int64(maxJitter))
		}
	}
	// A mid-range frac must land strictly inside the window: frac(ab12cd34) ≈
	// 0.67 → jitter ≈ 20s on a 5-minute schedule.
	got, ok := JitteredNextCronRunMs(cron, from, "ab12cd34")
	if !ok {
		t.Fatal("JitteredNextCronRunMs(ab12cd34) !ok")
	}
	if got <= t1 || got >= t1+30000 {
		t.Errorf("mid-frac jitter = %d, want strictly inside (t1, t1+30s) with t1=%d", got, t1)
	}
	// Pinned annual cron: gap huge, jitter pinned at the cap for any frac
	// above 0.2857 — the reachable consequence of CC's t2-proportional jitter.
	annual := "30 14 28 2 *"
	a1, ok := NextCronRunMs(annual, from)
	if !ok {
		t.Fatal("annual NextCronRunMs failed")
	}
	capped, ok := JitteredNextCronRunMs(annual, from, "ffffffff")
	if !ok {
		t.Fatal("annual JitteredNextCronRunMs !ok")
	}
	if capped != a1+recurringCapMs {
		t.Errorf("annual cron all-F id: jittered = %d, want t1+cap = %d (cap branch)", capped, a1+recurringCapMs)
	}
}

func TestOneShotJitter(t *testing.T) {
	pinLocalCST(t)
	from := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local).UnixMilli()

	// :30-mark fire time → backward lead in [fromMs, t1).
	t1, ok := NextCronRunMs("30 * * * *", from)
	if !ok {
		t.Fatal("NextCronRunMs(30 * * * *) failed")
	}
	got, ok := OneShotJitteredNextCronRunMs("30 * * * *", from, "ffffffff")
	if !ok {
		t.Fatal("one-shot jitter !ok")
	}
	if got < from || got >= t1 {
		t.Errorf("one-shot on :30 mark = %d, want in [%d, %d)", got, from, t1)
	}
	// frac(ffffffff) = 1-2^-32 → lead = 89999.99998 ms, truncated to 89999.
	if want := t1 - 89999; got != want {
		t.Errorf("all-F id jittered = %d, want %d (lead 89999 ms)", got, want)
	}
	// Non-hex id → frac 0 → no lead, still on t1.
	got, ok = OneShotJitteredNextCronRunMs("30 * * * *", from, "zz12zz99")
	if !ok {
		t.Fatal("one-shot jitter (non-hex) !ok")
	}
	if got != t1 {
		t.Errorf("non-hex id on :30 mark = %d, want t1 = %d (frac 0)", got, t1)
	}
	// :07 fire time → minute-mod gate misses → exactly t1.
	got, ok = OneShotJitteredNextCronRunMs("7 * * * *", from, "ffffffff")
	if !ok {
		t.Fatal("one-shot jitter (:07) !ok")
	}
	if want := time.Date(2026, 11, 11, 10, 7, 0, 0, time.Local).UnixMilli(); got != want {
		t.Errorf("one-shot on :07 = %d, want %d (no jitter off the hot marks)", got, want)
	}
	// Task created inside its own lead window clamps to fromMs.
	justBefore := t1 - 10_000 // 10 s before the :30 fire
	got, ok = OneShotJitteredNextCronRunMs("30 * * * *", justBefore, "ffffffff")
	if !ok {
		t.Fatal("one-shot jitter (clamp) !ok")
	}
	if got != justBefore {
		t.Errorf("clamped one-shot = %d, want fromMs = %d", got, justBefore)
	}
}

func TestJitterFracPrefixParse(t *testing.T) {
	// JS parseInt(taskId.slice(0,8), 16) stops at the first non-hex char:
	// "12zz..." → 0x12, "zz12..." → NaN → 0. A full-string ParseUint would
	// return an error (→ 0) for "12zz" and diverge on the first value.
	if got := jitterFrac("12zz9999"); got != float64(0x12)/0x1_0000_0000 {
		t.Errorf("jitterFrac(12zz9999) = %v, want 0x12/2^32", got)
	}
	if got := jitterFrac("zz129999"); got != 0 {
		t.Errorf("jitterFrac(zz129999) = %v, want 0", got)
	}
	if got := jitterFrac(""); got != 0 {
		t.Errorf("jitterFrac(empty) = %v, want 0", got)
	}
	if got := jitterFrac("00000000"); got != 0 {
		t.Errorf("jitterFrac(00000000) = %v, want 0", got)
	}
	if got := jitterFrac("ffffffff"); got <= 0.9999999 {
		t.Errorf("jitterFrac(ffffffff) = %v, want ≈1", got)
	}
}

func TestIsRecurringTaskAged(t *testing.T) {
	now := int64(1700000000000)
	const week = RecurringMaxAgeMs
	if IsRecurringTaskAged(Task{ID: "x", Recurring: true, CreatedAt: now - week}, now, 0) {
		t.Error("maxAge 0 = unlimited, want false")
	}
	if IsRecurringTaskAged(Task{ID: "x", Recurring: true, Permanent: true, CreatedAt: now - 8*week}, now, week) {
		t.Error("permanent task must never age")
	}
	if IsRecurringTaskAged(Task{ID: "x", CreatedAt: now - 8*week}, now, week) {
		t.Error("one-shot must not hit the recurring age path")
	}
	if !IsRecurringTaskAged(Task{ID: "x", Recurring: true, CreatedAt: now - 8*week}, now, week) {
		t.Error("recurring 8 days old must be aged")
	}
	if IsRecurringTaskAged(Task{ID: "x", Recurring: true, CreatedAt: now - 6*24*3600*1000}, now, week) {
		t.Error("recurring 6 days old must not be aged")
	}
	// Exactly maxAge is aged (>= comparison, CC uses >=).
	if !IsRecurringTaskAged(Task{ID: "x", Recurring: true, CreatedAt: now - week}, now, week) {
		t.Error("recurring exactly maxAge old must be aged (>=)")
	}
}

func TestConstantsPin(t *testing.T) {
	t.Parallel()
	if MaxJobs != 50 {
		t.Errorf("MaxJobs = %d, want 50", MaxJobs)
	}
	if RecurringMaxAgeMs != 7*24*60*60*1000 {
		t.Errorf("RecurringMaxAgeMs = %d, want %d", RecurringMaxAgeMs, 7*24*60*60*1000)
	}
	if MaxAgeDays != 7 {
		t.Errorf("MaxAgeDays = %d, want 7", MaxAgeDays)
	}
	if recurringFrac != 0.1 {
		t.Errorf("recurringFrac = %v, want 0.1", recurringFrac)
	}
	if recurringCapMs != 15*60*1000 {
		t.Errorf("recurringCapMs = %d, want 900000", recurringCapMs)
	}
	if oneShotMaxMs != 90*1000 {
		t.Errorf("oneShotMaxMs = %d, want 90000", oneShotMaxMs)
	}
	if oneShotMinuteMod != 30 {
		t.Errorf("oneShotMinuteMod = %d, want 30", oneShotMinuteMod)
	}
}

func TestStoreWriteErrorSurfaced(t *testing.T) {
	// A regular file where the store dir should be: MkdirAll fails and the
	// error must surface (a swallowed error would report durable writes as
	// successful and lose the task).
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	s := storeFor(filepath.Join(blocker, "nested"))
	err := s.Add(Task{ID: "err00001", Cron: "0 9 * * *", Prompt: "p", CreatedAt: 1})
	if err == nil {
		t.Fatal("Add into a blocked dir succeeded, want error")
	}
}

func TestReadTasksFieldTypes(t *testing.T) {
	dir := t.TempDir()
	body := `{"tasks":[` +
		`{"id":"t1","cron":"0 9 * * *","prompt":"p","createdAt":1000,"lastFiredAt":"1700"},` +
		`{"id":"t2","cron":"0 9 * * *","prompt":"p","createdAt":1000,"recurring":"yes"},` +
		`{"id":"t3","cron":"0 9 * * *","prompt":"p","createdAt":1000,"permanent":"1"},` +
		`{"id":"t4","cron":"0 9 * * *","prompt":42,"createdAt":1000},` +
		`{"id":"t5","cron":"0 9 * * *","prompt":"p","createdAt":"1700"},` +
		`{"id":"t6","cron":"0 9 * * *","prompt":"p","createdAt":1000,"unknown":"field"}` +
		`]}`
	if err := os.WriteFile(filepath.Join(dir, tasksFileName), []byte(body), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := storeFor(dir)
	tasks := s.Read()
	if len(tasks) != 4 {
		t.Fatalf("Read = %d tasks, want 4 (t1/t2/t3 row-kept-field-dropped, t6; t4/t5 dropped)", len(tasks))
	}
	byID := map[string]Task{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	t1, ok := byID["t1"]
	if !ok {
		t.Fatal("t1 dropped entirely — wrong-type optional fields must drop the FIELD, not the row")
	}
	if t1.LastFiredAt != 0 {
		t.Errorf("t1 lastFiredAt from string = %d, want dropped (0)", t1.LastFiredAt)
	}
	if t2, ok := byID["t2"]; !ok || t2.Recurring {
		t.Errorf("t2 recurring from string = %v, want dropped", t2.Recurring)
	}
	if t3, ok := byID["t3"]; !ok || t3.Permanent {
		t.Errorf("t3 permanent from string = %v, want dropped", t3.Permanent)
	}
	if _, ok := byID["t4"]; ok {
		t.Error("t4 (prompt wrong type) kept, want dropped")
	}
	if _, ok := byID["t5"]; ok {
		t.Error("t5 (createdAt wrong type) kept, want dropped")
	}
	t6, ok := byID["t6"]
	if !ok || t6.ID != "t6" || t6.Prompt != "p" {
		t.Errorf("t6 (unknown fields) = %+v, want kept intact", t6)
	}
}

func TestStoreRoundTripPermanentAndLastFired(t *testing.T) {
	dir := t.TempDir()
	s := storeFor(dir)
	in := Task{ID: "perm1234", Cron: "0 9 * * *", Prompt: "p", CreatedAt: 1000, LastFiredAt: 2000, Recurring: true, Permanent: true}
	if err := s.Add(in); err != nil {
		t.Fatalf("Add: %v", err)
	}
	tasks := s.Read()
	if len(tasks) != 1 {
		t.Fatalf("Read = %d tasks, want 1", len(tasks))
	}
	got := tasks[0]
	if got != in {
		t.Errorf("round-trip = %+v, want %+v", got, in)
	}
}

func TestJitterExactValues(t *testing.T) {
	pinLocalCST(t)
	from := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local).UnixMilli()
	const cron = "*/5 * * * *"
	t1, ok := NextCronRunMs(cron, from)
	if !ok {
		t.Fatal("NextCronRunMs failed")
	}
	// frac(80000000) = 0x80000000/2^32 = exactly 0.5 → jitter = 0.5*0.1*gap.
	got, ok := JitteredNextCronRunMs(cron, from, "80000000")
	if !ok {
		t.Fatal("JitteredNextCronRunMs failed")
	}
	if want := t1 + 15000; got != want {
		t.Errorf("recurring jitter = %d, want exactly %d (t1+0.5*0.1*300000)", got, want)
	}
	// One-shot on the :00 minute mark (10:05): lead = 0.5*90000 = 45000.
	t1Hour, ok := NextCronRunMs("30 * * * *", from)
	if !ok {
		t.Fatal("NextCronRunMs(hourly :30) failed")
	}
	got, ok = OneShotJitteredNextCronRunMs("30 * * * *", from, "80000000")
	if !ok {
		t.Fatal("OneShotJitteredNextCronRunMs failed")
	}
	if want := t1Hour - 45000; got != want {
		t.Errorf("one-shot jitter = %d, want exactly %d (t1-45000)", got, want)
	}
}

func TestFindMissedBoundary(t *testing.T) {
	pinLocalCST(t)
	nowMs := time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local).UnixMilli()
	// Next fire lands exactly at now → strictly-before means NOT missed.
	tasks := []Task{{ID: "edge", Cron: "0 12 * * *", Prompt: "p", CreatedAt: nowMs - 3600*1000}}
	if missed := FindMissedTasks(tasks, nowMs); len(missed) != 0 {
		t.Errorf("next == now counted as missed: %+v", missed)
	}
}

func TestJitterFracUppercaseHex(t *testing.T) {
	lower := jitterFrac("ffffffff")
	upper := jitterFrac("FFFFFFFF")
	if lower != upper {
		t.Errorf("jitterFrac case sensitivity: lower = %v, upper = %v, want equal", lower, upper)
	}
	if upper <= 0.9999999 {
		t.Errorf("jitterFrac(FFFFFFFF) = %v, want ≈1", upper)
	}
}

// TestStoreConcurrentMutationStress drives concurrent Add/Remove/MarkFired/
// Read on the process-shared store (the shape of two engines sharing a
// projectspace). Every read-modify-write must serialize (no Add lost, the
// concurrent Remove lands) and concurrent Read must never observe MarkFired
// stamping the cache in place (copy-before-mutate). The stamper and remover
// hit ids seeded BEFORE the storm, so their writes are real (not no-op
// misses) and genuinely overlap the adders. The final state is deterministic:
// phase 2 runs single-threaded.
func TestStoreConcurrentMutationStress(t *testing.T) {
	dir := t.TempDir()
	s := storeFor(dir)
	for _, id := range []string{"seed0001", "rmv00000", "rmv00001", "mrk00000", "mrk00001"} {
		if err := s.Add(Task{ID: id, Cron: "0 9 * * *", Prompt: "seed", CreatedAt: 1000, Recurring: true}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	const stamp = int64(1700000600000)

	// Phase 1: two Adders + a SINGLE-SHOT pair of MarkFired stampers + a
	// SINGLE-SHOT pair of Removers, plus a reader that keeps iterating
	// whatever Read hands out. The same-method pairs are the deterministic
	// serialization probe: two barrier-started mutations of one kind
	// read-read-write-write, and only the held lock forces the second
	// refresh to see the first's write (a lost stamp or a resurrected
	// removal is an unlocked RMW window).
	var mutWG sync.WaitGroup
	for g := range 2 {
		prefix := string(rune('a' + g))
		mutWG.Add(1)
		go func(prefix string) {
			defer mutWG.Done()
			for i := range 10 {
				id := prefix + "00000" + strconv.Itoa(i)
				if err := s.Add(Task{ID: id, Cron: "0 9 * * *", Prompt: "p", CreatedAt: 1000, Recurring: true}); err != nil {
					t.Errorf("Add %s: %v", id, err)
					return
				}
			}
		}(prefix)
	}
	startPairs := make(chan struct{})
	for _, mc := range []struct {
		id string
		at int64
	}{{"mrk00000", 111}, {"mrk00001", 222}} {
		mutWG.Add(1)
		go func(id string, at int64) {
			defer mutWG.Done()
			<-startPairs
			if err := s.MarkFired([]string{id}, at); err != nil {
				t.Errorf("MarkFired %s: %v", id, err)
			}
		}(mc.id, mc.at)
	}
	for _, rmID := range []string{"rmv00000", "rmv00001"} {
		mutWG.Add(1)
		go func(id string) {
			defer mutWG.Done()
			<-startPairs
			if err := s.Remove([]string{id}); err != nil {
				t.Errorf("Remove %s: %v", id, err)
			}
		}(rmID)
	}
	close(startPairs)
	stopRead := make(chan struct{})
	var readWG sync.WaitGroup
	readWG.Go(func() {
		for {
			select {
			case <-stopRead:
				return
			default:
			}
			for _, task := range s.Read() {
				if task.ID == "" {
					t.Error("reader observed an empty id")
					return
				}
			}
		}
	})

	// Phase 2: quiesce the mutators, stop the reader, then deterministic
	// Remove + batched MarkFired (the mrk rows keep their phase-1 stamps).
	mutWG.Wait()
	close(stopRead)
	readWG.Wait()
	if err := s.Remove([]string{"a000009"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	var remaining []string
	for _, task := range s.Read() {
		if task.ID == "mrk00000" || task.ID == "mrk00001" {
			continue
		}
		remaining = append(remaining, task.ID)
	}
	if err := s.MarkFired(remaining, stamp); err != nil {
		t.Fatalf("MarkFired all: %v", err)
	}

	// seed + mrk00000 + mrk00001 + a000000..a000008 + b000000..b000009 = 22 rows.
	tasks := s.Read()
	if len(tasks) != 22 {
		names := []string{}
		for _, task := range tasks {
			names = append(names, task.ID)
		}
		t.Fatalf("after stress: %d tasks, want 22 (lost writes?): %v", len(tasks), names)
	}
	inFile := map[string]Task{}
	for _, task := range tasks {
		inFile[task.ID] = task
	}
	for _, id := range remaining {
		if inFile[id].LastFiredAt != stamp {
			t.Errorf("task %s LastFiredAt = %d, want %d", id, inFile[id].LastFiredAt, stamp)
		}
	}
	// The racing stampers' single writes both survived (an unlocked RMW
	// would drop one — the surviving file would hold only one stamp).
	if inFile["mrk00000"].LastFiredAt != 111 {
		t.Errorf("mrk00000 LastFiredAt = %d, want 111 (the racing stamp was lost)", inFile["mrk00000"].LastFiredAt)
	}
	if inFile["mrk00001"].LastFiredAt != 222 {
		t.Errorf("mrk00001 LastFiredAt = %d, want 222 (the racing stamp was lost)", inFile["mrk00001"].LastFiredAt)
	}
	if inFile["a000009"].ID != "" {
		t.Error("a000009 survived the phase-2 Remove")
	}
	for _, rmID := range []string{"rmv00000", "rmv00001"} {
		if inFile[rmID].ID != "" {
			t.Errorf("%s survived the concurrent Remove (an unlocked RMW resurrected it)", rmID)
		}
	}
	inFileSet := map[string]bool{}
	for id := range inFile {
		inFileSet[id] = true
	}
	// The file on disk matches the cache (write-through held).
	for _, task := range readTasksFile(filepath.Join(dir, tasksFileName)) {
		if !inFileSet[task.ID] {
			t.Errorf("file holds %s, absent from cache read", task.ID)
		}
	}
}

// TestStoreMutationSeesExternalEdits pins the refreshLocked call at the top
// of every read-modify-write: a file edit by another process between our
// reads must fold into the mutation's snapshot, never be clobbered by the
// stale cache.
func TestStoreMutationSeesExternalEdits(t *testing.T) {
	// externalRow builds one row as "another process" would write it.
	externalRow := func(id string, createdAt int64) string {
		return `{"id":"` + id + `","cron":"0 9 * * *","prompt":"written by another gbot process, longer than the seeded row","createdAt":` + strconv.FormatInt(createdAt, 10) + `}`
	}
	tasksBody := func(rows ...string) string {
		return `{"tasks":[` + strings.Join(rows, ",") + `]}`
	}
	taskFile := func(dir string) string { return filepath.Join(dir, tasksFileName) }

	// Add folds the external row in.
	dir := t.TempDir()
	s := storeFor(dir)
	if err := s.Add(Task{ID: "exta0001", Cron: "0 9 * * *", Prompt: "p", CreatedAt: 1000}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if tasks := s.Read(); len(tasks) != 1 {
		t.Fatalf("prime: %d tasks, want 1", len(tasks))
	}
	if err := os.WriteFile(taskFile(dir), []byte(tasksBody(externalRow("extb0002", 2000))), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	if err := s.Add(Task{ID: "exta0003", Cron: "0 9 * * *", Prompt: "p", CreatedAt: 1000}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := map[string]bool{}
	for _, task := range s.Read() {
		got[task.ID] = true
	}
	if !got["extb0002"] || !got["exta0003"] || got["exta0001"] {
		t.Errorf("Add after external edit = %v, want {extb0002, exta0003}", got)
	}

	// Remove keeps the external row while dropping its target (the target
	// also survives the external write, so the Remove has real work).
	dir = t.TempDir()
	s = storeFor(dir)
	if err := s.Add(Task{ID: "r0000001", Cron: "0 9 * * *", Prompt: "p", CreatedAt: 1000}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if tasks := s.Read(); len(tasks) != 1 {
		t.Fatalf("prime: %d tasks, want 1", len(tasks))
	}
	if err := os.WriteFile(taskFile(dir), []byte(tasksBody(
		`{"id":"r0000001","cron":"0 9 * * *","prompt":"survivor","createdAt":1000}`,
		externalRow("extb0002", 2000),
	)), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	if err := s.Remove([]string{"r0000001"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	tasks := s.Read()
	if len(tasks) != 1 || tasks[0].ID != "extb0002" {
		t.Errorf("Remove after external edit = %+v, want only extb0002", tasks)
	}

	// MarkFired stamps its target without dropping the external row.
	dir = t.TempDir()
	s = storeFor(dir)
	if err := s.Add(Task{ID: "m0000001", Cron: "0 9 * * *", Prompt: "p", CreatedAt: 1000, Recurring: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if tasks := s.Read(); len(tasks) != 1 {
		t.Fatalf("prime: %d tasks, want 1", len(tasks))
	}
	if err := os.WriteFile(taskFile(dir), []byte(tasksBody(
		`{"id":"m0000001","cron":"0 9 * * *","prompt":"survivor","createdAt":1000}`,
		externalRow("extb0002", 2000),
	)), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	if err := s.MarkFired([]string{"m0000001"}, 1700000600000); err != nil {
		t.Fatalf("MarkFired: %v", err)
	}
	byID := map[string]Task{}
	for _, task := range s.Read() {
		byID[task.ID] = task
	}
	if len(byID) != 2 {
		t.Fatalf("MarkFired after external edit = %d tasks, want 2", len(byID))
	}
	if m := byID["m0000001"]; m.LastFiredAt != 1700000600000 {
		t.Errorf("m0000001 LastFiredAt = %d, want 1700000600000", m.LastFiredAt)
	}
}
