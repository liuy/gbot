package context_test

import (
	"strings"
	"testing"

	ctxbuild "github.com/liuy/gbot/pkg/context"
)

func TestAppendVoiceBlock(t *testing.T) {
	got := ctxbuild.AppendVoiceBlock("BASE")
	if want := "BASE\n\n" + ctxbuild.VoicePromptBlock; got != want {
		t.Errorf("AppendVoiceBlock(BASE) = %q, want %q", got, want)
	}
}

// Empty base must yield the block itself, not a leading blank line.
func TestAppendVoiceBlock_EmptyBase(t *testing.T) {
	got := ctxbuild.AppendVoiceBlock("")
	if want := ctxbuild.VoicePromptBlock; got != want {
		t.Errorf("AppendVoiceBlock(\"\") = %q, want the block itself", got)
	}
}

// The six OpenAI Realtime guide sections must all be present, by title.
func TestVoicePromptBlock_SixSections(t *testing.T) {
	for _, title := range []string{
		"## Language", "## Reasoning", "## Preambles",
		"## Verbosity", "## Unclear Audio", "## Tools",
	} {
		if !strings.Contains(ctxbuild.VoicePromptBlock, "\n"+title+"\n") {
			t.Errorf("VoicePromptBlock missing section %q", title)
		}
	}
}

// 10-27 ruling: the Language section's first line replaces the OpenAI
// English default with the multilingual variant.
func TestVoicePromptBlock_LanguageFirstLine(t *testing.T) {
	const marker = "## Language\n\n"
	i := strings.Index(ctxbuild.VoicePromptBlock, marker)
	if i < 0 {
		t.Fatal("VoicePromptBlock has no ## Language section")
	}
	rest := ctxbuild.VoicePromptBlock[i+len(marker):]
	line := rest
	if before, _, ok := strings.Cut(rest, "\n"); ok {
		line = before
	}
	if line != "Respond in the user's language." {
		t.Errorf("Language first line = %q, want %q", line, "Respond in the user's language.")
	}
}

// The Speech Output gate is the message marker (10-28 final ruling): replies
// to "[voice] "-marked messages are spoken prose, unmarked messages get
// normal markdown replies. The gate line's marker literal is derived from
// VoiceMsgMarker — same package — so the constant and the prompt cannot
// drift apart silently.
func TestVoicePromptBlock_SpeechOutputMarkerGate(t *testing.T) {
	b := ctxbuild.VoicePromptBlock
	if !strings.Contains(b, "# Speech Output\n") {
		t.Fatal("VoicePromptBlock missing top-level # Speech Output section")
	}
	if !strings.Contains(b, "carrying the \""+ctxbuild.VoiceMsgMarker+"\" marker") {
		t.Errorf("Speech Output gate must name the marker literal position-independently (VoiceMsgMarker = %q; injectTimestamp prefixes user messages, so the marker is never message-initial)", ctxbuild.VoiceMsgMarker)
	}
	if !strings.Contains(b, "read aloud by text-to-speech") {
		t.Error("Speech Output gate missing the spoken-prose clause for marked messages")
	}
	if strings.Contains(b, "Unmarked messages") {
		t.Error("Speech Output gate still carries the redundant unmarked-messages line (removed 10-28: default behavior needs no instruction)")
	}
	if !strings.Contains(b, "- Code, tables, and lists still render in the chat window and are\n  skipped automatically when read aloud.") {
		t.Error("Speech Output second bullet missing or altered")
	}
	if !strings.Contains(b, "The \""+ctxbuild.VoiceMsgMarker+"\" marker only labels the user's spoken messages. Never\n  write it, or any similar tag, in your own replies.") {
		t.Error("Speech Output must forbid the model from echoing the marker in its own replies (10-30: the model mimicked the user-message prefix and TTS read it aloud)")
	}
}

// Appendix A is verbatim law: the preamble Avoid list must stay word-for-word
// as written in the plan (an earlier revision silently shortened one phrase).
func TestVoicePromptBlock_PreambleAvoidList(t *testing.T) {
	for _, phrase := range []string{
		"- \"Let me think about that for a second.\"",
		"- \"Please wait while I process your request.\"",
		"- \"I'm going to use my tools now.\"",
		"- \"Interesting question. I will reason through this carefully.\"",
	} {
		if !strings.Contains(ctxbuild.VoicePromptBlock, phrase) {
			t.Errorf("VoicePromptBlock preamble Avoid list missing or altered: %q", phrase)
		}
	}
}
