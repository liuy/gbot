// Copyright 2026 Conductor OSS
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
// the License. You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
// an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
// specific language governing permissions and limitations under the License.

package markitdown

import (
	"errors"
	"io"
	"testing"
)

func TestPlainTextConstructorReturnsInstance(t *testing.T) {
	if NewPlainTextConverter() == nil {
		t.Fatal("NewPlainTextConverter() = nil, want non-nil")
	}
}

func TestPlainTextConvertReadErrorPreservesCause(t *testing.T) {
	c := NewPlainTextConverter()
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".txt"})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read error must wrap the reader error via %%w, got: %v", err)
	}
}

// Every scoring category and boundary rune is pinned by exact totals:
// score = confidence + 5*commonCJK + 1*rareCJK + latin - 10*replacement - 5*control.
func TestScoreDecodedTextExact(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"hello", 55},   // latin only
		{"A", 51},       // latin lower boundary
		{"z", 51},       // latin upper boundary
		{"0", 50},       // digits are not latin letters
		{"@", 50},       // just below latin range
		{"{", 50},       // just above latin range
		{"\u3040", 55},  // hiragana lower boundary
		{"\u30FF", 55},  // katakana upper boundary
		{"\u303F", 50},  // just below hiragana
		{"\u3100", 50},  // just above katakana
		{"\uFF00", 55},  // fullwidth lower boundary
		{"\uFFEF", 55},  // fullwidth upper boundary
		{"\uFEFF", 50},  // just below fullwidth
		{"\uFFF0", 50},  // just above fullwidth
		{"\u4E00", 55},  // common han boundary (一)
		{"\u4DFF", 50},  // just below han range
		{"\uA000", 50},  // just above han range
		{"\u4E01", 51},  // rare han not in common set
		{"\u9FFF", 51},  // han upper boundary, rare glyph
		{"\uFFFD", 40},  // replacement penalty
		{"\x01", 45},    // control penalty
		{"\x1f", 45},    // control upper boundary
		{" ", 50},       // space is not control
		{"\x7f", 50},    // DEL is not control
		{"\n", 50},      // newline exempt from control
		{"\r", 50},      // CR exempt from control
		{"\t", 50},      // TAB exempt from control
		{"a\uFFFD", 41}, // replacement weight
		{"a\x01", 46},   // control weight
		{"a\u3042", 56}, // common CJK weight
		{"a\u4E01", 52}, // rare CJK weight
		{"a", 51},       // latin weight
	}
	for _, tc := range cases {
		if got := scoreDecodedText(tc.text, 50); got != tc.want {
			t.Errorf("scoreDecodedText(%q, 50) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

// Detection must decode Shift-JIS bytes to the original text (chardet
// proposes several candidates; the scoring picks Japanese).
func TestDecodeWithDetectionShiftJIS(t *testing.T) {
	sjis := []byte{0x93, 0xfa, 0x96, 0x7b, 0x8c, 0xea} // 日本語
	if got := decodeWithDetection(sjis); got != "日本語" {
		t.Errorf("decodeWithDetection(sjis) = %q, want 日本語", got)
	}
}

// Bytes chardet cannot classify fall back to the raw bytes, not to an
// empty string.
func TestDecodeWithDetectionUndetectableFallsBackToRaw(t *testing.T) {
	got := decodeWithDetection([]byte{0x96, 0x96})
	if got != "\x96\x96" {
		t.Errorf("decodeWithDetection(undetectable) = %q, want raw bytes", got)
	}
}

// Valid UTF-8 that already contains U+FFFD is not trusted as-is for
// non-latin text: it goes back through detection, so the result must differ
// from the raw passthrough a validity-only shortcut would produce.
func TestDecodeWithDetectionValidFFFDGoesThroughDetection(t *testing.T) {
	data := []byte("Привет\ufffdмир") // valid UTF-8, Cyrillic + U+FFFD
	if got := decodeWithDetection(data); got == string(data) {
		t.Errorf("decodeWithDetection(valid-utf8-with-FFFD) = raw passthrough %q, want re-detected text", got)
	}
}
