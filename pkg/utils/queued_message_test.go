package utils

import (
	"testing"

	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/types"
)

func TestUnwrapQueuedReminder(t *testing.T) {
	const raw = "wait, also run the tests"
	envelope := "<system-reminder>\nThe user sent a new message while you were working:\n" + raw +
		"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>"

	got, ok := UnwrapQueuedReminder(envelope)
	if !ok {
		t.Fatal("envelope not recognized")
	}
	if got != raw {
		t.Errorf("unwrapped = %q, want %q", got, raw)
	}
	if !IsQueuedEnvelope(envelope) {
		t.Error("IsQueuedEnvelope(envelope) = false, want true")
	}
}

func TestUnwrapQueuedReminder_NonMatches(t *testing.T) {
	nonMatches := []string{
		"",
		"plain user text",
		"<system-reminder>\nThe user sent a new message while you were working:\nno suffix",
		"prefix only\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>",
		// The job origin shares the envelope shape but different wrap text.
		"<system-reminder>\nA background agent completed a job:\ndone\n</system-reminder>",
		// Wrapped envelope with leading timestamp (injectTimestamp form) must
		// NOT match — only the exact envelope does.
		"[2026-10-05 11:00:00 CST] <system-reminder>\nThe user sent a new message while you were working:\nhi" +
			"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>",
	}
	for _, text := range nonMatches {
		if _, ok := UnwrapQueuedReminder(text); ok {
			t.Errorf("UnwrapQueuedReminder(%q) matched, want no match", text)
		}
		if IsQueuedEnvelope(text) {
			t.Errorf("IsQueuedEnvelope(%q) = true, want false", text)
		}
	}
}

// TestIsSelectableUserMessage_QueuedMessageNotSelectable must skip queued message messages: they are persisted
// user input, but treating them as rewind targets would let auto-rewind throw
// the mid-turn message back into the input box on abort.
func TestIsSelectableUserMessage_QueuedMessageNotSelectable(t *testing.T) {
	msg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock(
			"<system-reminder>\nThe user sent a new message while you were working:\nhi" +
				"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>")},
	}
	if IsSelectableUserMessage(msg) {
		t.Error("queued message envelope must not be selectable for rewind")
	}
}

// bashJobXML mirrors pkg/tool/bash/background.go FormatXML output for a
// completed command (escapeXML escapes the quotes in the summary).
const bashJobXML = "<job-notification>\n<job-id>bg-1</job-id>\n<status>completed</status>\n" +
	"<summary>Background command &quot;make check&quot; completed (exit code 0)</summary>\n</job-notification>"

// agentJobXML mirrors pkg/tool/agent/fork.go:282-294 output shape.
const agentJobXML = "<job-notification>\n<job-id>agent-1</job-id>\n<tool-use-id>toolu_1</tool-use-id>\n" +
	"<status>completed</status>\n<agent-type>fork</agent-type>\n<duration-ms>1200</duration-ms>\n" +
	"<tokens>42</tokens>\n<tool-uses>1</tool-uses>\n<summary>Background agent &quot;refactor auth&quot; completed</summary>\n" +
	"<result>\ndone\n</result>\n</job-notification>"

const bashJobEnvelope = "<system-reminder>\nA background command completed:\n" + bashJobXML + "\n</system-reminder>"
const agentJobEnvelope = "<system-reminder>\nA background agent completed a job:\n" + agentJobXML + "\n</system-reminder>"

// Real bash producer shapes (FormatXML) — the sniff positives must be the
// bytes the engine actually enqueues, never hand-written approximations.
func bashDoneNotificationXML() string {
	return bash.JobNotification{
		JobID:      "bg-1",
		ToolUseID:  "toolu_9",
		Status:     "completed",
		Summary:    `Background command "make check" completed (exit code 0)`,
		OutputFile: "/tmp/bg-1.out",
	}.FormatXML()
}

func bashStallNotificationXML() string {
	return bash.JobNotification{
		JobID:   "bg-2",
		Summary: `Background command "read -p" appears stalled`,
		IsStall: true,
		// The tail is raw command output: a literal <system-reminder> in it
		// must not fail N3 — the check only covers the field segment.
		Tail: "Proceed? [y/n] <system-reminder>",
	}.FormatXML()
}

// T1 is the Go half of the one-sniff-spec contract (plan D7): the case list
// here is exactly the list R5 asserts in web/ui/src/reminder_render.test.ts,
// so a divergence between the Go and TS predicates fails in whichever
// implementation drifted.
func TestIsInjectedReminderEnvelope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"bash job envelope", bashJobEnvelope, true},
		{"agent job envelope", agentJobEnvelope, true},
		{"bare bash job notification", bashDoneNotificationXML(), true},
		{"bare bash stall notification", bashStallNotificationXML(), true},
		{"bare agent job notification", agentJobXML, true},
		{"timestamped assembled form must not match", "[2026-10-13 13:43:05 CST] " + bashJobEnvelope, false},
		{"job envelope missing suffix", "<system-reminder>\nA background command completed:\n" + bashJobXML, false},
		{"user queued envelope is not a job reminder", "<system-reminder>\nThe user sent a new message while you were working:\nhi" +
			"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>", false},
		{"message delivery is not a reminder envelope", "<message from=\"main\">\nhi\n</message>", false},
		{"empty text", "", false},
	}
	for _, tc := range cases {
		if got := IsInjectedReminderEnvelope(tc.text); got != tc.want {
			t.Errorf("%s: IsInjectedReminderEnvelope = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIsJobNotificationEnvelope pins the bare single-layer form (D2 N1-N3):
// head newline at index 0, terminator at the closing tag or the stall joint,
// and no unescaped system-reminder tag inside the field segment.
func TestIsJobNotificationEnvelope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"bash completion", bashDoneNotificationXML(), true},
		{"bash stall with joint and reminder-bearing tail", bashStallNotificationXML(), true},
		{"stall joint immediately after the head", "<job-notification>\n</job-notification>\nLast output: still waiting", true},
		{"agent shape", agentJobXML, true},
		{"timestamped prefix violates the index-0 rule", "[2026-10-14 09:00:00 CST] " + bashDoneNotificationXML(), false},
		{"missing head newline", "<job-notification><job-id>bg-1</job-id>\n</job-notification>", false},
		{"missing terminator", "<job-notification>\n<job-id>bg-1</job-id>", false},
		{"unescaped system-reminder inside the field segment", "<job-notification>\n<summary>s</summary>\n<system-reminder>x</system-reminder>\n</job-notification>", false},
		{"system-reminder inside fields before a stall joint", "<job-notification>\n<system-reminder>x</system-reminder></job-notification>\nLast output: tail", false},
		{"prose before head", "job done — " + bashDoneNotificationXML(), false},
		{"插话 envelope is not a job notification", "<system-reminder>\nThe user sent a new message while you were working:\nhi" +
			"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>", false},
		{"message delivery envelope", "<message from=\"main\">\nhi\n</message>", false},
		{"legacy wrapped envelope is not bare", bashJobEnvelope, false},
		{"empty text", "", false},
	}
	for _, tc := range cases {
		if got := IsJobNotificationEnvelope(tc.text); got != tc.want {
			t.Errorf("%s: IsJobNotificationEnvelope = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIsLegacyJobReminderEnvelope pins the extracted pre-single-layer body of
// the total predicate: exactly the two job heads plus the reminder suffix.
func TestIsLegacyJobReminderEnvelope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"bash job envelope", bashJobEnvelope, true},
		{"agent job envelope", agentJobEnvelope, true},
		{"bare bash form", bashJobXML, false},
		{"bare agent form", agentJobXML, false},
		{"bare producer stall", bashStallNotificationXML(), false},
		{"timestamped assembled form", "[2026-10-13 13:43:05 CST] " + bashJobEnvelope, false},
		{"插话 envelope", "<system-reminder>\nThe user sent a new message while you were working:\nhi" +
			"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>", false},
		{"message delivery envelope", "<message from=\"main\">\nhi\n</message>", false},
		{"empty text", "", false},
	}
	for _, tc := range cases {
		if got := IsLegacyJobReminderEnvelope(tc.text); got != tc.want {
			t.Errorf("%s: IsLegacyJobReminderEnvelope = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsMessageDeliveryEnvelope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"sender main", "<message from=\"main\">\nhi\n</message>", true},
		{"sender e2", "<message from=\"e2\">\nhi\n</message>", true},
		{"sender agent-3", "<message from=\"agent-3\">\nhi\n</message>", true},
		{"sender fork-2", "<message from=\"fork-2\">\nhi\n</message>", true},
		{"space in sender", "<message from=\"a b\">hi</message>", false},
		{"empty sender", "<message from=\"\">hi</message>", false},
		{"non-ascii sender", "<message from=\"用户\">hi</message>", false},
		{"prose after closing tag", "<message from=\"main\">\nhi\n</message> and then some prose", false},
		{"unescaped reminder inside body", "<message from=\"main\">\n<system-reminder>x</system-reminder>\n</message>", false},
		{"missing closing tag", "<message from=\"x\">hi", false},
		// The envelope prefix is required on its own merits: a text whose
		// Cut-fallback path would satisfy every later clause (id-shaped
		// sender before the quote, correct tail) must still not classify.
		{"quoted id without the envelope prefix", "nope\"main\">\n</message>", false},
		{"timestamped prefix", "[2026-10-13 13:43:05 CST] <message from=\"main\">\nhi\n</message>", false},
		{"job envelope is not a delivery", bashJobEnvelope, false},
		{"plain user text", "hello world", false},
		{"empty text", "", false},
		// Every allowed charset boundary character at once (both edges of
		// each range) — kills per-range mutations individually.
		{"sender with every id boundary char", "<message from=\"azAZ09._-\">hi</message>", true},
	}
	for _, tc := range cases {
		if got := IsMessageDeliveryEnvelope(tc.text); got != tc.want {
			t.Errorf("%s: IsMessageDeliveryEnvelope = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestMessageDeliverySender pins the TUI-side label source: the sender id is
// cut from the `<message from="X">` head, empty when the head shape is absent.
// Only the head matters — a non-envelope text with a valid head still yields
// its sender, because the full-envelope gate lives in IsMessageDeliveryEnvelope.
func TestMessageDeliverySender(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want string
	}{
		{"sender main", "<message from=\"main\">\nhi\n</message>", "main"},
		{"sender with every id boundary char", "<message from=\"azAZ09._-\">hi</message>", "azAZ09._-"},
		{"sender with escaped quote in body", "<message from=\"fork-2\">\nhe said &quot;hi&quot;\n</message>", "fork-2"},
		{"no head", "hello world", ""},
		{"head without closing quote", "<message from=\"main", ""},
		// A quote-bearing text WITHOUT the head prefix must stay empty — kills
		// a dropped prefix guard, which would otherwise cut at the prose quote.
		{"prose with a quote but no head", "he said \"hi\"", ""},
		{"empty text", "", ""},
	}
	for _, tc := range cases {
		if got := MessageDeliverySender(tc.text); got != tc.want {
			t.Errorf("%s: MessageDeliverySender = %q, want %q", tc.name, got, tc.want)
		}
	}
}
