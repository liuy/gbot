package utils

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool/bash"
)

// agentResultXML mirrors pkg/tool/agent/fork.go buildAgentNotificationXML
// output for result Content `all green "ok"` + newline + `second line`
// (xe = xml.Escape: numeric quotes, &#xA; newlines). The literal↔builder byte
// pin lives in pkg/tool/agent/fork_test.go — utils cannot import agent
// (import cycle).
const agentResultXML = "<job-notification>\n<job-id>agent-1</job-id>\n<tool-use-id>toolu_1</tool-use-id>\n" +
	"<status>completed</status>\n<agent-type>fork</agent-type>\n<duration-ms>1200</duration-ms>\n" +
	"<tokens>42</tokens>\n<tool-uses>1</tool-uses>\n<summary>Background agent \"refactor auth\" completed</summary>\n" +
	"<result>\nall green &#34;ok&#34;&#xA;second line\n</result>\n</job-notification>"

// agentEmptyResultXML: same builder with Content "" → <result>\n\n</result>,
// an all-whitespace result body that must collapse to the summary line only.
const agentEmptyResultXML = "<job-notification>\n<job-id>agent-2</job-id>\n<tool-use-id>toolu_2</tool-use-id>\n" +
	"<status>completed</status>\n<agent-type>fork</agent-type>\n<duration-ms>50</duration-ms>\n" +
	"<tokens>3</tokens>\n<tool-uses>0</tool-uses>\n<summary>Background agent \"cleanup\" completed</summary>\n" +
	"<result>\n\n</result>\n</job-notification>"

// stallBoilerplate is the fixed tail background.go appends after the stall
// joint — accepted display content, byte-pinned from the format string.
const stallBoilerplate = "The command is likely blocked on an interactive prompt. Kill this job and re-run with piped input (e.g., `echo y | command`) or a non-interactive flag if one exists."

// TestExtractXMLTag pins the TS-extractTag-mirroring scan directly — the
// boundary positions (opening tag at index 0, empty span) are unreachable
// through EnvelopeDisplayText's public shapes but must not drift from the
// first-span semantics the TS twin relies on.
func TestExtractXMLTag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		text   string
		tag    string
		want   string
		wantOK bool
	}{
		{"opening tag at index 0", "<summary>a</summary>", "summary", "a", true},
		{"empty span", "<summary></summary>", "summary", "", true},
		{"span in the middle", "pre<job-id>x</job-id>post", "job-id", "x", true},
		{"closing tag before opening is skipped", "</summary>x<summary>a</summary>", "summary", "a", true},
		{"opening absent", "plain", "summary", "", false},
		{"closing absent", "<summary>x", "summary", "", false},
		{"empty text", "", "summary", "", false},
	}
	for _, tc := range cases {
		got, ok := extractXMLTag(tc.text, tc.tag)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
		}
		if got != tc.want {
			t.Errorf("%s: extractXMLTag = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEnvelopeDisplayText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		// Arm 1 — message delivery: decoded body only, no from line.
		{
			"message delivery with full escape alphabet and CJK",
			"<message from=\"e2\">\nq&#34;q a&#39;l l&lt;t g&gt;t 配置 tab[&#x9;] nl[&#xA;] cr[&#xD;] amp &amp;\n</message>",
			"q\"q a'l l<t g>t 配置 tab[\t] nl[\n] cr[\r] amp &",
			true,
		},
		{
			// The producer escapes raw `&quot;` to &amp;quot; — one decode
			// pass must leave it as &quot;, never a second-level quote.
			"literal entity text single-decodes",
			"<message from=\"main\">\nsay &amp;quot;hi&amp;quot;\n</message>",
			"say &quot;hi&quot;",
			true,
		},
		// Arm 2 — bare job notification: summary line + result body,
		// machine fields hidden.
		{
			"agent notification with escaped result",
			agentResultXML,
			"Background agent \"refactor auth\" completed\n\nall green \"ok\"\nsecond line",
			true,
		},
		{
			"agent notification with empty result keeps summary only",
			agentEmptyResultXML,
			"Background agent \"cleanup\" completed",
			true,
		},
		{
			"bash completion is summary line only",
			bash.JobNotification{
				JobID:      "bg-1",
				ToolUseID:  "toolu_9",
				Status:     "completed",
				Summary:    `Background command "make check" completed (exit code 0)`,
				OutputFile: "/tmp/bg-1.out",
			}.FormatXML(),
			"Background command \"make check\" completed (exit code 0)",
			true,
		},
		{
			"bash stall renders joint tail with boilerplate",
			bash.JobNotification{
				JobID:   "bg-2",
				Summary: `Background command "read -p" appears stalled`,
				IsStall: true,
				Tail:    "Proceed? [y/n]",
			}.FormatXML(),
			"Background command \"read -p\" appears stalled\n\nProceed? [y/n]\n\n" + stallBoilerplate,
			true,
		},
		{
			// Producer-shaped: summary escaped (named quotes/apostrophe),
			// tail interpolated RAW — its &amp; must survive verbatim while
			// the summary still decodes.
			"bash stall tail stays verbatim, summary decodes",
			bash.JobNotification{
				JobID:   "bg-3",
				Summary: `Background command "can't wait" appears stalled`,
				IsStall: true,
				Tail:    "got &amp; input",
			}.FormatXML(),
			"Background command \"can't wait\" appears stalled\n\ngot &amp; input\n\n" + stallBoilerplate,
			true,
		},
		{
			"bare notification without a summary tag falls back to inner segment",
			"<job-notification>\n<job-id>bg-9</job-id>\n</job-notification>",
			"<job-id>bg-9</job-id>",
			true,
		},
		{
			"unclosed summary tag falls back to inner segment",
			"<job-notification>\n<summary>partial\n</job-notification>",
			"<summary>partial",
			true,
		},
		// Arm 3 — single-layer system-reminder: body only.
		{
			"generic system-reminder body",
			"<system-reminder>\njust a note\n</system-reminder>",
			"just a note",
			true,
		},
		{
			// Pins the caller-order contract: arm 3 also matches 插话 and
			// returns the full body including the IMPORTANT boilerplate.
			"插话 envelope matches arm 3 with boilerplate body",
			"<system-reminder>\nThe user sent a new message while you were working:\nhi" +
				"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.\n</system-reminder>",
			"The user sent a new message while you were working:\nhi" +
				"\n\nIMPORTANT: After completing your current task, you MUST address the user's message above. Do not ignore it.",
			true,
		},
		// Negatives — ok=false.
		{"legacy wrapped job envelope", bashJobEnvelope, "", false},
		{"plain text", "hello world", "", false},
		{"bare job-notification without head newline", "<job-notification><job-id>bg-1</job-id>\n</job-notification>", "", false},
		{"system-reminder head without tail", "<system-reminder>\njust a note", "", false},
		{"system-reminder without head newline", "<system-reminder>just a note</system-reminder>", "", false},
	}
	for _, tc := range cases {
		got, ok := EnvelopeDisplayText(tc.in)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
		}
		if got != tc.want {
			t.Errorf("%s: EnvelopeDisplayText = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Machine fields never leak into notification display text.
	out, ok := EnvelopeDisplayText(agentResultXML)
	if !ok {
		t.Fatal("agentResultXML not recognized")
	}
	for _, tag := range []string{"<job-id>", "<tool-use-id>", "<status>", "<agent-type>", "<duration-ms>", "<tokens>", "<tool-uses>", "<job-notification>"} {
		if strings.Contains(out, tag) {
			t.Errorf("agent display leaks %s: %q", tag, out)
		}
	}

	// Delivery display never leaks the envelope head or the from attribute.
	out, ok = EnvelopeDisplayText("<message from=\"main\">\nsay &#34;hi&#34;\n</message>")
	if !ok {
		t.Fatal("message delivery not recognized")
	}
	if strings.Contains(out, "<message") || strings.Contains(out, "from=\"") {
		t.Errorf("delivery display leaks envelope head: %q", out)
	}
}
