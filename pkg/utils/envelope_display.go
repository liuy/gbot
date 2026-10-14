package utils

import "strings"

// systemReminderHead is arm 3's head; the tail is reminderSuffix (shared with
// the legacy job heads, which arm 3 must exclude).
const systemReminderHead = "<system-reminder>\n"

// xmlUnescaper decodes the union of both producers' escape alphabets in ONE
// left-to-right pass — bash escapeXML emits the named five (&quot; &apos;
// &lt; &gt; &amp;), the agent/message xe (xml.Escape) emits the numeric forms
// (&#34; &#39;) and escapes newline/tab/CR (&#xA; &#x9; &#xD;). &amp; is the
// only pattern whose output can start another pattern's input, so the single
// pass is what prevents &amp;quot; from double-decoding.
var xmlUnescaper = strings.NewReplacer(
	"&#34;", `"`,
	"&#39;", "'",
	"&quot;", `"`,
	"&apos;", "'",
	"&lt;", "<",
	"&gt;", ">",
	"&#xA;", "\n",
	"&#x9;", "\t",
	"&#xD;", "\r",
	"&amp;", "&",
)

func xmlUnescape(s string) string { return xmlUnescaper.Replace(s) }

// extractXMLTag mirrors the TS extractTag semantics: the first <tag>…</tag>
// span anywhere in text (shortest span from the first opening tag). ok=false
// when the tag is absent or never closed.
func extractXMLTag(text, tag string) (string, bool) {
	head := "<" + tag + ">"
	i := strings.Index(text, head)
	if i < 0 {
		return "", false
	}
	rest := text[i+len(head):]
	j := strings.Index(rest, "</"+tag+">")
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// EnvelopeDisplayText reduces a recognized single-layer envelope to display
// text: message → decoded body only; job-notification → summary line + result
// body (machine fields hidden); system-reminder → body only. ok=false for
// anything else, including the legacy double-wrapped job form (no recursion by
// design) — callers keep the legacy skip/raw treatment.
//
// Caller order is load-bearing: arm 3 also matches the mid-turn queued-message
// (插话) envelope and returns its full body including the IMPORTANT boilerplate;
// callers wanting the original text must try UnwrapQueuedReminder FIRST.
func EnvelopeDisplayText(text string) (string, bool) {
	if IsMessageDeliveryEnvelope(text) {
		return messageDeliveryDisplay(text), true
	}
	if IsJobNotificationEnvelope(text) {
		return jobNotificationDisplay(text), true
	}
	if !IsLegacyJobReminderEnvelope(text) {
		if inner, ok := strings.CutPrefix(text, systemReminderHead); ok {
			if body, ok := strings.CutSuffix(inner, reminderSuffix); ok {
				return body, true
			}
		}
	}
	return "", false
}

// messageDeliveryDisplay returns the decoded body: text between the
// `<message from="X">` head and </message> with the bounding newlines dropped.
// The producer always emits `\nbody\n`; the single-bound trim also keeps the
// predicate-true-but-newline-less paste total.
func messageDeliveryDisplay(text string) string {
	inner, _ := strings.CutPrefix(text, messageDeliveryPrefix)
	_, rest, _ := strings.Cut(inner, `"`)
	body, _ := strings.CutPrefix(rest, ">")
	body, _ = strings.CutSuffix(body, messageDeliverySuffix)
	body = strings.TrimPrefix(body, "\n")
	body = strings.TrimSuffix(body, "\n")
	return xmlUnescape(body)
}

// jobNotificationDisplay renders summary line + result body. Machine fields
// (job-id, tool-use-id, status, agent-type, output-file, duration-ms, tokens,
// tool-uses) are hidden entirely — duration/tokens are dropped, not compacted.
// The stall tail is interpolated raw by the producer (background.go), so it is
// appended verbatim and NEVER entity-decoded: decoding would corrupt
// legitimate entity-shaped output (`echo '&amp;'`).
func jobNotificationDisplay(text string) string {
	summary, ok := extractXMLTag(text, "summary")
	if !ok {
		// Malformed-but-matching envelope: fall back to the trimmed field
		// segment so the function stays total.
		summary, _ = jobNotificationSegment(text)
		summary = strings.TrimSpace(summary)
	}
	out := xmlUnescape(summary)
	if result, ok := extractXMLTag(text, "result"); ok {
		body := xmlUnescape(result)
		body = strings.TrimPrefix(body, "\n")
		body = strings.TrimSuffix(body, "\n")
		if strings.TrimSpace(body) != "" {
			out += "\n\n" + body
		}
	}
	if _, after, ok := strings.Cut(text, jobStallJoint); ok {
		out += "\n\n" + strings.TrimSpace(after)
	}
	return out
}
