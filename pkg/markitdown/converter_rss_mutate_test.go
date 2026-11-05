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
	"strings"
	"testing"
)

func TestRSSConstructorReturnsInstance(t *testing.T) {
	if NewRSSConverter() == nil {
		t.Fatal("NewRSSConverter() = nil, want non-nil")
	}
}

func TestRSSConvertParseErrorPreservesCause(t *testing.T) {
	c := NewRSSConverter()
	_, err := c.Convert(strings.NewReader("not a feed"), StreamInfo{Extension: ".rss"})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("parse error must wrap the gofeed error via %%w, got: %v", err)
	}
}

func TestRSSConvertMinimalFeed(t *testing.T) {
	// No description, no item dates: pins the exact skeleton so stray
	// conditionals add or drop visible lines.
	feed := `<?xml version="1.0"?><rss version="2.0"><channel><title>Only Title</title><item><title>I1</title></item></channel></rss>`
	c := NewRSSConverter()
	result, err := c.Convert(strings.NewReader(feed), StreamInfo{Extension: ".rss"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want := "# Only Title\n\n## I1\n\n"
	if result.Markdown != want {
		t.Errorf("Markdown = %q, want %q", result.Markdown, want)
	}
}

func TestRSSConvertFeedDescriptionIncluded(t *testing.T) {
	feed := `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><description>Feed description</description><item><title>I1</title></item></channel></rss>`
	c := NewRSSConverter()
	result, err := c.Convert(strings.NewReader(feed), StreamInfo{Extension: ".rss"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "Feed description\n") {
		t.Errorf("Markdown should contain feed description, got: %q", result.Markdown)
	}
}

// JSON Feed is the one shape gofeed maps to item.Updated with an empty
// item.Published; the converter must print it as "Updated:".
func TestRSSConvertUpdatedFallback(t *testing.T) {
	jf := `{"version":"https://jsonfeed.org/version/1","title":"T","items":[{"title":"I1","date_modified":"2024-06-01T00:00:00Z"}]}`
	c := NewRSSConverter()
	result, err := c.Convert(strings.NewReader(jf), StreamInfo{Extension: ".rss"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "Updated: 2024-06-01T00:00:00Z") {
		t.Errorf("item with only updated date should print Updated:, got: %q", result.Markdown)
	}
}

func TestRSSConvertPublishedWinsOverUpdated(t *testing.T) {
	jf := `{"version":"https://jsonfeed.org/version/1","title":"T","items":[{"title":"I1","date_published":"2024-01-01T00:00:00Z","date_modified":"2024-06-01T00:00:00Z"}]}`
	c := NewRSSConverter()
	result, err := c.Convert(strings.NewReader(jf), StreamInfo{Extension: ".rss"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "Published: 2024-01-01T00:00:00Z") {
		t.Errorf("published date must win, got: %q", result.Markdown)
	}
	if strings.Contains(result.Markdown, "Updated:") {
		t.Errorf("updated must not print when published exists, got: %q", result.Markdown)
	}
}

func TestRSSConvertDescriptionFillsEmptyContent(t *testing.T) {
	feed := `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><item><title>I1</title><description>fallback description</description></item></channel></rss>`
	c := NewRSSConverter()
	result, err := c.Convert(strings.NewReader(feed), StreamInfo{Extension: ".rss"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "fallback description") {
		t.Errorf("empty content must fall back to description, got: %q", result.Markdown)
	}
}

// Only content containing both angle brackets is treated as HTML: half-open
// text like "a < b" or "1 > 0" must survive verbatim.
func TestRSSConvertPartialMarkupKeptLiteral(t *testing.T) {
	feed := `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title>` +
		`<item><title>lt</title><description>a &lt; b</description></item>` +
		`<item><title>gt</title><description>1 &gt; 0</description></item>` +
		`<item><title>html</title><description>&lt;b&gt;bold&lt;/b&gt;</description></item>` +
		`</channel></rss>`
	c := NewRSSConverter()
	result, err := c.Convert(strings.NewReader(feed), StreamInfo{Extension: ".rss"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	for _, want := range []string{"a < b", "1 > 0", "**bold**"} {
		if !strings.Contains(result.Markdown, want) {
			t.Errorf("Markdown should contain %q, got: %q", want, result.Markdown)
		}
	}
}
