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

func TestHTMLAcceptsXHTMLMime(t *testing.T) {
	c := NewHTMLConverter(nil)
	if !c.Accepts(StreamInfo{MIMEType: "application/xhtml+xml"}) {
		t.Error("Accepts(application/xhtml+xml) = false, want true")
	}
	if !c.Accepts(StreamInfo{MIMEType: "text/html"}) {
		t.Error("Accepts(text/html) = false, want true")
	}
}

func TestHTMLConvertReadErrorPreservesCause(t *testing.T) {
	c := NewHTMLConverter(nil)
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".html"})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read error must wrap the reader error via %%w, got: %v", err)
	}
}

// A comment node whose text is "title" must not be mistaken for the title
// element: the type check in the matcher and the early return in the walker
// both have to survive contact with it.
func TestExtractHTMLTitleIgnoresTitleComment(t *testing.T) {
	got := extractHTMLTitle(`<html><head><!--title--><title>Real</title></head><body></body></html>`)
	if got != "Real" {
		t.Errorf("extractHTMLTitle = %q, want %q", got, "Real")
	}
}

// The walker stops at the first title element: a second one later in the
// document must not overwrite it.
func TestExtractHTMLTitleFirstTitleWins(t *testing.T) {
	got := extractHTMLTitle(`<html><head><title>First</title></head><body><title>Second</title></body></html>`)
	if got != "First" {
		t.Errorf("extractHTMLTitle = %q, want %q", got, "First")
	}
}
