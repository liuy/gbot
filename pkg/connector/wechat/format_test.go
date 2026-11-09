package wechat

import (
	"strings"
	"testing"
)

func pinRepeat(r rune, n int) string { return strings.Repeat(string(r), n) }

// Blank-line collapsing must apply outside code fences only, and the blank-run
// counter must reset at every fence so the first blank after a fence survives.
func TestPinNormalizeMarkdownBlocks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"collapse-three", "a\n\n\nb", "a\n\nb"},
		{"keep-single", "a\n\nb", "a\n\nb"},
		{"fence-reset", "a\n\n\n```\nx\n```\n\n\nb", "a\n\n```\nx\n```\n\nb"},
		{"inside-fence-preserved", "```\n\n\nx\n```", "```\n\n\nx\n```"},
	}
	for _, c := range cases {
		got := normalizeMarkdownBlocks(c.in)
		if got != c.want {
			t.Errorf("%s: normalizeMarkdownBlocks(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// A code block that was hard-split leaves `current` empty, so the closing
// fence must not be emitted as a lone chunk; the next opening fence must still
// be emitted (it is not a closing fence).
func TestPinSplitHardSplitCodeFences(t *testing.T) {
	a := pinRepeat('a', 4500)
	b := pinRepeat('b', 4500)
	text := "```\n" + a + "\n```\n```\n" + b + "\n```\ntail"
	want := []string{
		"```\n```",
		"```\n" + a[:3992] + "\n```",
		"```\n" + a[3992:] + "\n```",
		"```\n```",
		"```\n" + b[:3992] + "\n```",
		"```\n" + b[3992:] + "\n```",
		"tail",
	}
	got := splitForWeChat(text)
	if len(got) != len(want) {
		t.Fatalf("len(chunks) = %d, want %d\nchunks=%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk[%d] = %q..., want %q...", i, pinClip(got[i]), pinClip(want[i]))
		}
	}
}

// A closing fence reached with content still buffered must be kept in that
// chunk — the skip rule applies only when the buffer is empty.
func TestPinSplitClosingFenceWithBuffer(t *testing.T) {
	c := pinRepeat('c', 3000)
	d := pinRepeat('d', 1500)
	text := "```\n" + c + "\n" + d + "\n```\ntail"
	want := []string{
		"```\n" + c + "\n```",
		"```\n" + d + "\n```\ntail",
	}
	got := splitForWeChat(text)
	if len(got) != len(want) {
		t.Fatalf("len(chunks) = %d, want %d\nchunks=%q", len(got), len(want), pinClipAll(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk[%d] = %q..., want %q...", i, pinClip(got[i]), pinClip(want[i]))
		}
	}
}

// The flush trigger is a strict overflow: a line that lands the buffer exactly
// on the limit stays in the current chunk.
func TestPinSplitFlushBoundaryExact(t *testing.T) {
	l1 := pinRepeat('q', 3997)
	text := l1 + "\ny\nz"
	want := []string{l1 + "\ny", "z"}
	got := splitForWeChat(text)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("chunks = %q, want %q", pinClipAll(got), pinClipAll(want))
	}
}

// A single buffered line is still a flushable chunk: the guard is "any
// buffered lines", not "more than one".
func TestPinSplitFlushWithSingleLine(t *testing.T) {
	l1 := pinRepeat('1', 3000)
	l2 := pinRepeat('2', 1500)
	l3 := pinRepeat('3', 1500)
	text := l1 + "\n" + l2 + "\n" + l3
	want := []string{l1, l2 + "\n" + l3}
	got := splitForWeChat(text)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("chunks = %q, want %q", pinClipAll(got), pinClipAll(want))
	}
}

// flush() must reset the running length to exactly zero: an off-by-one there
// shifts every later chunk boundary by one line. The leading short line makes
// flush run with a non-empty buffer, which is where the reset happens.
func TestPinSplitFlushResetsLen(t *testing.T) {
	h := pinRepeat('h', 4001)
	m := pinRepeat('m', 999)
	text := "short\n" + h + "\n" + m + "\n" + m + "\n" + m + "\n" + m + "\n" + m
	want := []string{
		"short", h[:4000], h[4000:],
		m + "\n" + m + "\n" + m + "\n" + m,
		m,
	}
	got := splitForWeChat(text)
	if len(got) != len(want) {
		t.Fatalf("len(chunks) = %d, want %d\nchunks=%q", len(got), len(want), pinClipAll(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk[%d] = %q..., want %q...", i, pinClip(got[i]), pinClip(want[i]))
		}
	}
}

// Same reset, opposite direction: the boundary must not move one line later.
func TestPinSplitFlushResetsLenDec(t *testing.T) {
	h := pinRepeat('h', 4001)
	m := pinRepeat('m', 999)
	n := pinRepeat('n', 998)
	text := "short\n" + h + "\n" + m + "\n" + m + "\n" + m + "\n" + n + "\nx\n" + m
	want := []string{
		"short", h[:4000], h[4000:],
		m + "\n" + m + "\n" + m + "\n" + n,
		"x\n" + m,
	}
	got := splitForWeChat(text)
	if len(got) != len(want) {
		t.Fatalf("len(chunks) = %d, want %d\nchunks=%q", len(got), len(want), pinClipAll(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk[%d] = %q..., want %q...", i, pinClip(got[i]), pinClip(want[i]))
		}
	}
}

// A fence language tag long enough to leave exactly one rune of budget must
// still take the reopen path (budget 1), one rune longer must fall back to the
// plain hard split (budget 0), and the fallback must emit every piece.
func TestPinSplitCodeBlockBudget(t *testing.T) {
	x := pinRepeat('x', 4500)

	one := pinRepeat('L', 3991)
	textOne := "```" + one + "\n" + x + "\n```"
	gotOne := splitForWeChat(textOne)
	if len(gotOne) != 4501 {
		t.Fatalf("budget=1: len(chunks) = %d, want 4501 (one rune of code per chunk)", len(gotOne))
	}
	if want := "```" + one + "\nx\n```"; gotOne[1] != want {
		t.Errorf("budget=1: chunk[1] = %q..., want %q...", pinClip(gotOne[1]), pinClip(want))
	}

	zero := pinRepeat('L', 3992)
	textZero := "```" + zero + "\n" + x + "\ntail\n```"
	gotZero := splitForWeChat(textZero)
	wantZero := []string{"```" + zero + "\n```", x[:4000], x[4000:], "tail\n```"}
	if len(gotZero) != len(wantZero) {
		t.Fatalf("budget=0: len(chunks) = %d, want %d\nchunks=%q", len(gotZero), len(wantZero), pinClipAll(gotZero))
	}
	for i := range wantZero {
		if gotZero[i] != wantZero[i] {
			t.Errorf("budget=0: chunk[%d] = %q..., want %q...", i, pinClip(gotZero[i]), pinClip(wantZero[i]))
		}
	}
}

func pinClip(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:20] + "..." + s[len(s)-20:]
}

func pinClipAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = pinClip(s)
	}
	return out
}
