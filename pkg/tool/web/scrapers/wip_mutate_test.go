package scrapers

import "testing"

// The markdown pipeline is a chain of rewrites; asserting the final string
// pins every stage at once, since dropping any one stage changes the result.
func TestHTMLToBasicMarkdown_AllStages(t *testing.T) {
	input := "<script>var a = 1;</script>" +
		"<style>.x{color:red}</style>" +
		"<p>Alpha <b>bold</b></p>\n\n\n\n" +
		"<p>Beta<br>Gamma<br/>Delta<br />Epsilon</p>" +
		"<p>&lt;tag&gt;&amp;&quot;&#039;&#39;&#x27;&#x2F;&nbsp;tail</p>"
	want := "Alpha bold\n\nBeta\nGamma\nDelta\nEpsilon\n\n<tag>&\"'''/ tail"

	got := htmlToBasicMarkdown(input)
	if got != want {
		t.Errorf("htmlToBasicMarkdown() = %q, want %q", got, want)
	}
}

func TestHTMLToBasicMarkdown_CollapsesThreePlusNewlines(t *testing.T) {
	got := htmlToBasicMarkdown("a\n\n\n\n\n\nb")
	if got != "a\n\nb" {
		t.Errorf("htmlToBasicMarkdown() = %q, want %q", got, "a\n\nb")
	}
}

func TestDecodeHTMLEntities_EachEntity(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"&lt;", "<"},
		{"&gt;", ">"},
		{"&amp;", "&"},
		{"&quot;", `"`},
		{"&#039;", "'"},
		{"&#39;", "'"},
		{"&#x27;", "'"},
		{"&#x2F;", "/"},
		{"&nbsp;", " "},
		{"&unknown;", "&unknown;"},
	}
	for _, tt := range tests {
		got := decodeHTMLEntities(tt.input)
		if got != tt.want {
			t.Errorf("decodeHTMLEntities(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
