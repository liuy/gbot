package short

import (
	"strings"
	"testing"
)

// prompt.go:271,289 — the compact prompt is preamble + base + custom
// instructions + trailer, in that order, with nothing replaced along the way.
func TestGetCompactPrompt_ExactComposition(t *testing.T) {
	want := noToolsPreamble + baseCompactPrompt +
		"\n\nAdditional Instructions:\nfocus on the tests" +
		noToolsTrailer
	if got := GetCompactPrompt("focus on the tests"); got != want {
		t.Errorf("GetCompactPrompt mismatch:\n got %q\nwant %q", got, want)
	}
}

// prompt.go:287,289 — same composition for the partial prompt, with the
// up_to template selected.
func TestGetPartialCompactPrompt_ExactComposition(t *testing.T) {
	want := noToolsPreamble + partialCompactUpToPrompt +
		"\n\nAdditional Instructions:\nkeep the plan" +
		noToolsTrailer
	if got := GetPartialCompactPrompt("keep the plan", "up_to"); got != want {
		t.Errorf("GetPartialCompactPrompt(up_to) mismatch:\n got %q\nwant %q", got, want)
	}

	wantFrom := noToolsPreamble + partialCompactPrompt + noToolsTrailer
	if got := GetPartialCompactPrompt("", "from"); got != wantFrom {
		t.Errorf("GetPartialCompactPrompt(from) mismatch:\n got %q\nwant %q", got, wantFrom)
	}
}

// prompt.go:312 — runs of blank lines collapse to exactly one blank line.
func TestFormatCompactSummary_CollapsesBlankRuns(t *testing.T) {
	raw := "<analysis>draft</analysis>\n\n\n\n<summary>first\n\n\n\nsecond</summary>"
	want := "Summary:\nfirst\n\nsecond"
	if got := FormatCompactSummary(raw); got != want {
		t.Errorf("FormatCompactSummary = %q, want %q", got, want)
	}
}

const summaryHeader = "This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation."

// prompt.go:329 — the transcript-path sentence is appended to the base.
func TestGetCompactUserSummaryMessage_TranscriptPathAppended(t *testing.T) {
	want := summaryHeader + "\n\nSummary:\nDone things." +
		"\n\nIf you need specific details from before compaction (like exact code snippets, error messages, or content you generated), read the full transcript at: /tmp/transcript.jsonl"
	got := GetCompactUserSummaryMessage("<summary>Done things.</summary>", false, "/tmp/transcript.jsonl", "")
	if got != want {
		t.Errorf("transcript-path message mismatch:\n got %q\nwant %q", got, want)
	}
}

// prompt.go:333 — the preserved-messages note is appended to the base.
func TestGetCompactUserSummaryMessage_PreservedNoteAppended(t *testing.T) {
	want := summaryHeader + "\n\nSummary:\nDone things." +
		"\n\nRecent messages are preserved verbatim."
	got := GetCompactUserSummaryMessage("<summary>Done things.</summary>", false, "", "yes")
	if got != want {
		t.Errorf("preserved-note message mismatch:\n got %q\nwant %q", got, want)
	}
}

// prompt.go:337 — with follow-ups allowed the message stops at the base; the
// continuation instruction is only for suppressFollowUp.
func TestGetCompactUserSummaryMessage_AllowsFollowUpStopsAtBase(t *testing.T) {
	want := summaryHeader + "\n\nSummary:\nDone things."
	got := GetCompactUserSummaryMessage("<summary>Done things.</summary>", false, "", "")
	if got != want {
		t.Errorf("follow-up-allowed message mismatch:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "Continue the conversation from where it left off") {
		t.Errorf("follow-up-allowed message carries the continuation instruction: %q", got)
	}
}
