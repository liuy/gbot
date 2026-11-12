package cron

import (
	"testing"
	"time"
)

// pinLocalCST pins time.Local to a fixed +8 zone: computeNextCronRun and
// cronToHuman interpret cron fields in the process's local timezone, so the
// expected values below are only valid under a deterministic zone (same
// precedent as pkg/engine/persistence_test.go).
func pinLocalCST(t *testing.T) {
	t.Helper()
	orig := time.Local
	time.Local = time.FixedZone("CST-TEST", 8*3600)
	t.Cleanup(func() { time.Local = orig })
}

func mustFields(t *testing.T, expr string) *CronFields {
	t.Helper()
	f := ParseCronExpression(expr)
	if f == nil {
		t.Fatalf("ParseCronExpression(%q) = nil, want valid", expr)
	}
	return f
}

func assertInts(t *testing.T, name string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", name, got, want)
			return
		}
	}
}

func TestParseCronExpression_Valid(t *testing.T) {
	t.Parallel()
	f := mustFields(t, "*/5 * * * *")
	full := func(lo, hi int) []int {
		out := make([]int, 0, hi-lo+1)
		for i := lo; i <= hi; i++ {
			out = append(out, i)
		}
		return out
	}
	assertInts(t, "minute */5", f.Minute, []int{0, 5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55})
	assertInts(t, "hour *", f.Hour, full(0, 23))
	assertInts(t, "dom *", f.DayOfMonth, full(1, 31))
	assertInts(t, "month *", f.Month, full(1, 12))
	assertInts(t, "dow *", f.DayOfWeek, full(0, 6))

	f = mustFields(t, "30 14 28 2 *")
	assertInts(t, "minute 30", f.Minute, []int{30})
	assertInts(t, "hour 14", f.Hour, []int{14})
	assertInts(t, "dom 28", f.DayOfMonth, []int{28})
	assertInts(t, "month 2", f.Month, []int{2})

	f = mustFields(t, "0 9 * * 1-5")
	assertInts(t, "dow 1-5", f.DayOfWeek, []int{1, 2, 3, 4, 5})

	// dow 7 = Sunday alias, in both range and single forms.
	f = mustFields(t, "0 0 * * 5-7")
	assertInts(t, "dow 5-7", f.DayOfWeek, []int{0, 5, 6})
	f = mustFields(t, "0 0 * * 7")
	assertInts(t, "dow 7", f.DayOfWeek, []int{0})

	f = mustFields(t, "*/15 * * * *")
	assertInts(t, "minute */15", f.Minute, []int{0, 15, 30, 45})

	// Comma list dedups and sorts.
	f = mustFields(t, "5,1,5 0 * * *")
	assertInts(t, "minute list", f.Minute, []int{1, 5})

	// Range with step.
	f = mustFields(t, "1-30/10 * * * *")
	assertInts(t, "minute 1-30/10", f.Minute, []int{1, 11, 21})

	// dom/dow range crossing 7 still bounds-checked via effMax.
	f = mustFields(t, "0 0 * * 0-7")
	assertInts(t, "dow 0-7", f.DayOfWeek, []int{0, 1, 2, 3, 4, 5, 6})
}

func TestParseCronExpression_Invalid(t *testing.T) {
	t.Parallel()
	invalid := []string{
		"* * * *",         // 4 fields
		"* * * * * *",     // 6 fields
		"60 * * * *",      // minute out of range
		"*/0 * * * *",     // step < 1
		"5-1 * * * *",     // reversed range
		"0 0 32 * *",      // dom out of range
		"0 24 * * *",      // hour out of range
		"0 0 0 * *",       // dom below min
		"0 0 * 0 *",       // month below min
		"0 0 * 13 *",      // month above max
		"0 0 * * 8",       // dow above max (7 is the alias ceiling)
		"a * * * *",       // non-numeric
		"*,,* * * * *",    // empty list part
		"1-30/0 * * * *",  // range step < 1
		"0 0 * * 5-8",     // range hi above effMax
		"",                // empty
		"   ",             // whitespace only
		"0 9 * * * extra", // trailing junk
		"*/5/* * * * *",   // malformed step
		"1- 2 * * *",      // dangling range dash
	}
	for _, expr := range invalid {
		if f := ParseCronExpression(expr); f != nil {
			t.Errorf("ParseCronExpression(%q) = %+v, want nil", expr, f)
		}
	}
}

func TestComputeNextCronRun(t *testing.T) {
	pinLocalCST(t)
	at := func(y int, mo time.Month, d, h, mi, s int, ns int) time.Time {
		return time.Date(y, mo, d, h, mi, s, ns, time.Local)
	}

	f := mustFields(t, "*/5 * * * *")
	got := ComputeNextCronRun(f, at(2026, 11, 11, 10, 2, 30, 500000000))
	if got == nil {
		t.Fatal("ComputeNextCronRun(*/5) = nil, want 10:05")
	}
	if want := at(2026, 11, 11, 10, 5, 0, 0); !got.Equal(want) {
		t.Errorf("*/5 from 10:02:30.5 = %v, want %v", got, want)
	}

	// Boundary exclusion: from exactly 14:30:00 the next run is tomorrow.
	f = mustFields(t, "30 14 * * *")
	got = ComputeNextCronRun(f, at(2026, 11, 11, 14, 30, 0, 0))
	if want := at(2026, 11, 12, 14, 30, 0, 0); got == nil || !got.Equal(want) {
		t.Errorf("30 14 * * * from 14:30:00 = %v, want %v", got, want)
	}

	// dom/dow OR semantics when both restricted: from Monday 2026-11-16 the
	// next Friday (11-20) precedes the next 13th (12-13).
	f = mustFields(t, "0 0 13 * 5")
	got = ComputeNextCronRun(f, at(2026, 11, 16, 0, 0, 0, 0))
	if want := at(2026, 11, 20, 0, 0, 0, 0); got == nil || !got.Equal(want) {
		t.Errorf("0 0 13 * 5 from 2026-11-16 = %v, want %v", got, want)
	}

	// Pinned dom+month: Feb 28 may be next year.
	f = mustFields(t, "30 14 28 2 *")
	got = ComputeNextCronRun(f, at(2026, 11, 11, 10, 0, 0, 0))
	if want := at(2027, 2, 28, 14, 30, 0, 0); got == nil || !got.Equal(want) {
		t.Errorf("30 14 28 2 * from 2026-11-11 = %v, want %v", got, want)
	}

	// Impossible: Feb 31 never matches within the 366-day bound.
	f = mustFields(t, "0 0 31 2 *")
	if got := ComputeNextCronRun(f, at(2026, 11, 11, 10, 0, 0, 0)); got != nil {
		t.Errorf("0 0 31 2 * = %v, want nil", got)
	}

	// Hour jump: from 13:59 the next 14:xx lands same day.
	f = mustFields(t, "0 14 * * *")
	got = ComputeNextCronRun(f, at(2026, 11, 11, 13, 59, 0, 0))
	if want := at(2026, 11, 11, 14, 0, 0, 0); got == nil || !got.Equal(want) {
		t.Errorf("0 14 * * * from 13:59 = %v, want %v", got, want)
	}
}

func TestNextCronRunMs(t *testing.T) {
	pinLocalCST(t)
	if _, ok := NextCronRunMs("not a cron", 0); ok {
		t.Error("NextCronRunMs(invalid) ok, want !ok")
	}
	if _, ok := NextCronRunMs("0 0 31 2 *", atMs(t, 2026, 11, 11, 10, 0)); ok {
		t.Error("NextCronRunMs(0 0 31 2 *) ok, want !ok (366-day bound)")
	}
	from := time.Date(2026, 11, 11, 10, 2, 30, 500000000, time.Local)
	gotMs, ok := NextCronRunMs("*/5 * * * *", from.UnixMilli())
	if !ok {
		t.Fatal("NextCronRunMs(*/5) !ok, want ok")
	}
	want := time.Date(2026, 11, 11, 10, 5, 0, 0, time.Local)
	if gotMs != want.UnixMilli() {
		t.Errorf("NextCronRunMs(*/5) = %d, want %d (= ComputeNextCronRun)", gotMs, want.UnixMilli())
	}
}

func atMs(t *testing.T, y int, mo time.Month, d, h, mi int) int64 {
	t.Helper()
	return time.Date(y, mo, d, h, mi, 0, 0, time.Local).UnixMilli()
}

func TestFormatLocalTime24h(t *testing.T) {
	pinLocalCST(t)
	cases := []struct {
		minute, hour int
		want         string
	}{
		{30, 14, "14:30"},
		{0, 0, "00:00"},
		{5, 9, "09:05"},
		{59, 23, "23:59"},
		{18, 12, "12:18"},
	}
	for _, tc := range cases {
		if got := formatLocalTime(tc.minute, tc.hour); got != tc.want {
			t.Errorf("formatLocalTime(%d, %d) = %q, want %q", tc.minute, tc.hour, got, tc.want)
		}
	}
}

func TestCronToHuman(t *testing.T) {
	pinLocalCST(t)
	cases := []struct{ cron, want string }{
		{"*/1 * * * *", "Every minute"},
		{"*/5 * * * *", "Every 5 minutes"},
		{"0 * * * *", "Every hour"},
		{"7 * * * *", "Every hour at :07"},
		{"0 */1 * * *", "Every hour"},
		{"0 */2 * * *", "Every 2 hours"},
		{"5 */3 * * *", "Every 3 hours at :05"},
		{"30 14 * * *", "Every day at 14:30"},
		{"0 0 * * *", "Every day at 00:00"},
		{"0 9 * * 3", "Every Wednesday at 09:00"},
		{"0 9 * * 7", "Every Sunday at 09:00"},
		{"0 9 * * 1-5", "Weekdays at 09:00"},
		// Raw fallbacks.
		{"1,2 9 * * *", "1,2 9 * * *"},
		{"*/5 9 * * *", "*/5 9 * * *"},
		{"0 9 * * 1,3", "0 9 * * 1,3"},
		{"not a cron", "not a cron"},
	}
	for _, tc := range cases {
		if got := CronToHuman(tc.cron); got != tc.want {
			t.Errorf("CronToHuman(%q) = %q, want %q", tc.cron, got, tc.want)
		}
	}
}

// TestCronToHuman_PinnedDate covers the gbot-added arm: fully pinned
// dom+month with dow wildcard renders as "HH:MM, Mon D" (monthNames).
func TestCronToHuman_PinnedDate(t *testing.T) {
	pinLocalCST(t)
	cases := []struct{ cron, want string }{
		{"18 12 12 11 *", "12:18, Nov 12"},
		{"30 14 28 2 *", "14:30, Feb 28"},
		{"0 9 1 1 *", "09:00, Jan 1"},
		{"0 9 30 6 *", "09:00, Jun 30"},
		{"0 9 31 12 *", "09:00, Dec 31"},
		{"0 0 29 2 *", "00:00, Feb 29"},
		{"5 9 5 3 *", "09:05, Mar 5"},     // single-digit fields
		{"05 09 05 03 *", "09:05, Mar 5"}, // zero-padded fields normalize
		{"30 14 28 2 1", "30 14 28 2 1"},  // dow pinned → raw
		{"0 9 13 * *", "0 9 13 * *"},      // month wildcard → raw
		{"0 9 * 3 *", "0 9 * 3 *"},        // dom wildcard → raw
		{"0 9 1 13 *", "0 9 1 13 *"},      // month out of range → raw, no panic
		{"0 9 1 0 *", "0 9 1 0 *"},        // month zero → raw, no panic
		{"0 9 32 3 *", "09:00, Mar 32"},   // dom not range-checked (validation is upstream at create, same as the other arms)
		{"1,2 9 1 1 *", "1,2 9 1 1 *"},    // minute list → raw (earlier guard)
		{"*/5 9 1 1 *", "*/5 9 1 1 *"},    // minute step → raw (earlier guard)
		{"0 9 1 +3 *", "0 9 1 +3 *"},      // "+3" Atoi-parses but is not digits → raw
	}
	for _, tc := range cases {
		if got := CronToHuman(tc.cron); got != tc.want {
			t.Errorf("CronToHuman(%q) = %q, want %q", tc.cron, got, tc.want)
		}
	}
}

func TestParseCronExpression_Boundaries(t *testing.T) {
	t.Parallel()
	// Every field at its maximum: {0-59, 0-23, 1-31, 1-12, 0-6}.
	f := mustFields(t, "59 23 31 12 6")
	assertInts(t, "minute max", f.Minute, []int{59})
	assertInts(t, "hour max", f.Hour, []int{23})
	assertInts(t, "dom max", f.DayOfMonth, []int{31})
	assertInts(t, "month max", f.Month, []int{12})
	assertInts(t, "dow max", f.DayOfWeek, []int{6})
	// Every field at its minimum: dom/month minima are 1.
	f = mustFields(t, "0 0 1 1 0")
	assertInts(t, "dom min", f.DayOfMonth, []int{1})
	assertInts(t, "month min", f.Month, []int{1})
	assertInts(t, "dow min", f.DayOfWeek, []int{0})
}

func TestExpandField_MoreForms(t *testing.T) {
	t.Parallel()
	f := mustFields(t, "10-30/7 8-18/5 * * *")
	assertInts(t, "minute 10-30/7", f.Minute, []int{10, 17, 24})
	assertInts(t, "hour 8-18/5", f.Hour, []int{8, 13, 18})
	// dow range with step crosses the 7 alias: 1,3,5,7→0.
	f = mustFields(t, "0 0 * * 1-7/2")
	assertInts(t, "dow 1-7/2", f.DayOfWeek, []int{0, 1, 3, 5})
	// dow list mixing 6 and the 7 alias.
	f = mustFields(t, "0 0 * * 6,7")
	assertInts(t, "dow 6,7", f.DayOfWeek, []int{0, 6})
}

func TestComputeNextCronRun_WalkJumps(t *testing.T) {
	pinLocalCST(t)
	at := func(y int, mo time.Month, d, h, mi int) time.Time {
		return time.Date(y, mo, d, h, mi, 0, 0, time.Local)
	}
	// Month jump: next March 1st from mid-November crosses a year boundary.
	f := mustFields(t, "0 0 1 3 *")
	got := ComputeNextCronRun(f, at(2026, 11, 11, 10, 0))
	if want := at(2027, 3, 1, 0, 0); got == nil || !got.Equal(want) {
		t.Errorf("month jump = %v, want %v", got, want)
	}
	// dom-restricted only (dow wildcard): next 13th.
	f = mustFields(t, "0 0 13 * *")
	got = ComputeNextCronRun(f, at(2026, 11, 11, 10, 0))
	if want := at(2026, 11, 13, 0, 0); got == nil || !got.Equal(want) {
		t.Errorf("dom only = %v, want %v", got, want)
	}
	// dow-restricted only (dom wildcard): next Friday from Wednesday.
	f = mustFields(t, "0 0 * * 5")
	got = ComputeNextCronRun(f, at(2026, 11, 11, 10, 0))
	if want := at(2026, 11, 13, 0, 0); got == nil || !got.Equal(want) {
		t.Errorf("dow only = %v, want %v", got, want)
	}
	// Minute-rounding tightness: from 10:04:30 the next */5 match is 10:05 —
	// rounding that added two minutes instead would land on 10:10.
	f = mustFields(t, "*/5 * * * *")
	got = ComputeNextCronRun(f, time.Date(2026, 11, 11, 10, 4, 30, 500000000, time.Local))
	if want := at(2026, 11, 11, 10, 5); got == nil || !got.Equal(want) {
		t.Errorf("rounding = %v, want %v", got, want)
	}
	// Hour rollover in the initial +1 minute round: 23:59:30 → 00:00 next day.
	f = mustFields(t, "5 0 * * *")
	got = ComputeNextCronRun(f, time.Date(2026, 11, 11, 23, 59, 30, 0, time.Local))
	if want := at(2026, 11, 12, 0, 5); got == nil || !got.Equal(want) {
		t.Errorf("hour rollover = %v, want %v", got, want)
	}
}

func TestCronToHuman_MoreArms(t *testing.T) {
	pinLocalCST(t)
	cases := []struct{ cron, want string }{
		{"*/7 * * * *", "Every 7 minutes"},
		{"10 * * * *", "Every hour at :10"},
		{"0 */4 * * *", "Every 4 hours"},
		{"5 */1 * * *", "Every hour at :05"},
		{"0 9 * * 6", "Every Saturday at 09:00"},
		{"0 9 * * 0", "Every Sunday at 09:00"},
		{"0 9 * 3 *", "0 9 * 3 *"},        // month restricted → raw
		{"0 9 13 * *", "0 9 13 * *"},      // dom restricted → raw
		{"30 14 28 2 *", "14:30, Feb 28"}, // fully pinned → date form
	}
	for _, tc := range cases {
		if got := CronToHuman(tc.cron); got != tc.want {
			t.Errorf("CronToHuman(%q) = %q, want %q", tc.cron, got, tc.want)
		}
	}
}
