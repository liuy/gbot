package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

func TestParseSince_UnitsAndCap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"12h", 12 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"3m", 90 * 24 * time.Hour},
		{"1y", 365 * 24 * time.Hour},
		{"100000h", 100000 * time.Hour},
		// Overflow guard: anything past the cap clamps to 100000 units.
		{"999999h", 100000 * time.Hour},
		{"500000d", 100000 * 24 * time.Hour},
	}
	for _, c := range cases {
		got, err := parseSince(c.in)
		if err != nil {
			t.Fatalf("parseSince(%q): %v", c.in, err)
		}
		// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
		want := time.Now().Add(-c.want)
		if diff := want.Sub(got); diff > 2*time.Second || diff < -2*time.Second {
			t.Errorf("parseSince(%q) = %v, want ~%v", c.in, got, want)
		}
	}
	_, err := parseSince("7x")
	if err == nil {
		t.Fatal("invalid unit must error")
	}
	if err.Error() != `invalid since format "7x" (use e.g. '7d', '12h', '2w', '3m', '1y')` {
		t.Errorf("err = %q, want the pinned format message", err.Error())
	}
	zero, err := parseSince("")
	if err != nil || !zero.IsZero() {
		t.Errorf("parseSince(\"\") = %v, %v — want zero time, no error", zero, err)
	}
}

func TestMakeSnippet_Edges(t *testing.T) {
	t.Parallel()
	// maxRunes <= 0 disables truncation entirely.
	if got := makeSnippet("hello world", "hello", 0); got != "hello world" {
		t.Errorf("maxRunes 0 = %q, want the text unchanged", got)
	}
	// Text that fits is verbatim.
	if got := makeSnippet("hello", "ell", 5); got != "hello" {
		t.Errorf("fitting text = %q, want hello", got)
	}
	// No term found: window from the start with a trailing ellipsis.
	if got := makeSnippet("abcdefghij", "zzz", 4); got != "abcd..." {
		t.Errorf("no-term window = %q, want abcd...", got)
	}
	// Term centered: 10 runes, window 4, match "ef" at rune 4, center 5,
	// start 3 → "defg" with ellipses on both sides.
	if got := makeSnippet("abcdefghij", "ef", 4); got != "...defg..." {
		t.Errorf("centered window = %q, want ...defg...", got)
	}
	// Match near the end: the window slides back so start lands exactly on 0.
	if got := makeSnippet("abcdefghij", "ij", 10); got != "abcdefghij" {
		t.Errorf("end-aligned match = %q, want the full text", got)
	}
	if got := makeSnippet("abcdefghij", "ij", 6); got != "...efghij" {
		t.Errorf("end-aligned window = %q, want ...efghij", got)
	}
	// Rune-safe: multibyte text is windowed by runes, not bytes.
	if got := makeSnippet("一二三四五六七八九十", "九", 4); got != "...七八九十" {
		t.Errorf("cjk window = %q, want ...七八九十", got)
	}
}

// limit defaults to 50 and clamps at 200.
func TestExecute_LimitDefaults(t *testing.T) {
	store := openStore(t)
	sess, err := store.CreateSession("/test", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := range 250 {
		msg := &short.TranscriptMessage{
			UUID:      fmt.Sprintf("msg-%03d", i),
			Type:      "user",
			Content:   `[{"type":"text","text":"needle search term"}]`,
			CreatedAt: time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC),
		}
		if err := store.AppendMessage(sess.SessionID, msg); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	count := func(input string) int {
		t.Helper()
		res, err := execute(context.Background(), json.RawMessage(input), Deps{Store: store})
		if err != nil {
			t.Fatalf("execute(%s): %v", input, err)
		}
		return len(res.Data.(*Output).Messages)
	}
	if got := count(`{"query":"needle"}`); got != 50 {
		t.Errorf("default limit = %d, want 50", got)
	}
	if got := count(`{"query":"needle","limit":0}`); got != 50 {
		t.Errorf("limit 0 = %d, want the 50 default", got)
	}
	if got := count(`{"query":"needle","limit":-5}`); got != 50 {
		t.Errorf("negative limit = %d, want the 50 default", got)
	}
	if got := count(`{"query":"needle","limit":300}`); got != 200 {
		t.Errorf("limit 300 = %d, want the 200 clamp", got)
	}
	if got := count(`{"query":"needle","limit":7}`); got != 7 {
		t.Errorf("limit 7 = %d, want 7", got)
	}
}

func TestRecall_Describe(t *testing.T) {
	t.Parallel()
	r := New(nil, "")
	cases := []struct{ input, want string }{
		{`{"query":"alpha"}`, "alpha"},
		{`{"query":"alpha","since":"7d"}`, "alpha (7d)"},
		{`{"uuid":"abc"}`, "uuid: abc"},
		{`{"uuid":"abc","query":"alpha","since":"7d"}`, "uuid: abc"},
		{`{`, "Search conversation history"},
	}
	for _, c := range cases {
		got, err := r.Description(json.RawMessage(c.input))
		if err != nil {
			t.Fatalf("Describe(%s): %v", c.input, err)
		}
		if got != c.want {
			t.Errorf("Describe(%s) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestRecall_ToolFields(t *testing.T) {
	t.Parallel()
	r := New(nil, "")
	if got := r.Name(); got != "Recall" {
		t.Errorf("Name = %q, want Recall", got)
	}
	aliases := r.Aliases()
	if len(aliases) != 1 || aliases[0] != "recall" {
		t.Errorf("Aliases = %v, want [recall]", aliases)
	}
	if !strings.Contains(r.Prompt(), "Recall") {
		t.Errorf("Prompt = %q, want it to name the tool", r.Prompt())
	}
}
