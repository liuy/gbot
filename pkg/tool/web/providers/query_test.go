package providers

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func assertQueryEqual(t *testing.T, got, want StructuredQuery) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed query mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseSearchQuery_Plain(t *testing.T) {
	assertQueryEqual(t, ParseSearchQuery("golang generics tutorial"), StructuredQuery{
		Raw:   "golang generics tutorial",
		Text:  "golang generics tutorial",
		Terms: []QueryTerm{{Text: "golang"}, {Text: "generics"}, {Text: "tutorial"}},
	})
}

func TestParseSearchQuery_Sites(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  StructuredQuery
	}{
		{
			name:  "bare site with free text",
			input: "site:github.com bug",
			want: StructuredQuery{
				Raw:           "site:github.com bug",
				Text:          "bug",
				Terms:         []QueryTerm{{Text: "bug"}},
				Sites:         []string{"github.com"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "scheme stripped",
			input: "site:https://github.com",
			want: StructuredQuery{
				Raw: "site:https://github.com", Sites: []string{"github.com"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "path kept",
			input: "site:github.com/anthropics",
			want: StructuredQuery{
				Raw: "site:github.com/anthropics", Sites: []string{"github.com/anthropics"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "trailing slash and dot stripped",
			input: "site:github.com/.",
			want: StructuredQuery{
				Raw: "site:github.com/.", Sites: []string{"github.com"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "wildcard prefix stripped",
			input: "site:*.example.com",
			want: StructuredQuery{
				Raw: "site:*.example.com", Sites: []string{"example.com"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "domain and host aliases",
			input: "domain:example.com host:other.org",
			want: StructuredQuery{
				Raw: "domain:example.com host:other.org", Sites: []string{"example.com", "other.org"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "uppercase directive and value normalized",
			input: "SITE:GitHub.Com",
			want: StructuredQuery{
				Raw: "SITE:GitHub.Com", Sites: []string{"github.com"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "multiple sites both kept",
			input: "site:a.com site:b.com",
			want: StructuredQuery{
				Raw: "site:a.com site:b.com", Sites: []string{"a.com", "b.com"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "negated site",
			input: "-site:x.com",
			want: StructuredQuery{
				Raw: "-site:x.com", ExcludedSites: []string{"x.com"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "NOT before site negates",
			input: "NOT site:x.com",
			want: StructuredQuery{
				Raw: "NOT site:x.com", ExcludedSites: []string{"x.com"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "split dash does not negate following directive",
			input: "- site:x.com",
			want: StructuredQuery{
				Raw: "- site:x.com", Sites: []string{"x.com"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "site adopts next plain token as value",
			input: "site: example.com",
			want: StructuredQuery{
				Raw: "site: example.com", Sites: []string{"example.com"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "site does not adopt reserved token but flags directives",
			input: "site: OR",
			want: StructuredQuery{
				Raw:           "site: OR",
				HasDirectives: true,
			},
		},
		{
			name:  "internal paren keeps site whole",
			input: "site:wikipedia.org/Foo_(bar) x",
			want: StructuredQuery{
				Raw:           "site:wikipedia.org/Foo_(bar) x",
				Text:          "x",
				Terms:         []QueryTerm{{Text: "x"}},
				Sites:         []string{"wikipedia.org/foo_(bar)"},
				HasDirectives: true, HasConstraints: true,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertQueryEqual(t, ParseSearchQuery(c.input), c.want)
		})
	}
}

func TestParseSearchQuery_InURLTitleText(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  StructuredQuery
	}{
		{
			name:  "inurl aliases and negation",
			input: "inurl:releases url:docs -inurl:ads",
			want: StructuredQuery{
				Raw:           "inurl:releases url:docs -inurl:ads",
				InURL:         []string{"releases", "docs"},
				ExcludedInURL: []string{"ads"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "intitle aliases and negation",
			input: "intitle:weather title:forecast -intitle:spam",
			want: StructuredQuery{
				Raw:             "intitle:weather title:forecast -intitle:spam",
				InTitle:         []string{"weather", "forecast"},
				ExcludedInTitle: []string{"spam"},
				HasDirectives:   true, HasConstraints: true,
			},
		},
		{
			name:  "intext aliases and negation",
			input: "intext:climate inbody:warm inanchor:x -intext:y",
			want: StructuredQuery{
				Raw:            "intext:climate inbody:warm inanchor:x -intext:y",
				InText:         []string{"climate", "warm", "x"},
				ExcludedInText: []string{"y"},
				HasDirectives:  true,
			},
		},
		{
			name:  "quoted directive value swallowed whole",
			input: `intitle:"budget tips" debt`,
			want: StructuredQuery{
				Raw:           `intitle:"budget tips" debt`,
				Text:          "debt",
				Terms:         []QueryTerm{{Text: "debt"}},
				InTitle:       []string{"budget tips"},
				HasDirectives: true, HasConstraints: true,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertQueryEqual(t, ParseSearchQuery(c.input), c.want)
		})
	}
}

func TestParseSearchQuery_AllModes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  StructuredQuery
	}{
		{
			name:  "allintitle captures inline and following terms",
			input: "allintitle:climate report",
			want: StructuredQuery{
				Raw: "allintitle:climate report", InTitle: []string{"climate", "report"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "allinurl",
			input: "allinurl:go http",
			want: StructuredQuery{
				Raw: "allinurl:go http", InURL: []string{"go", "http"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "allintext not post-filterable",
			input: "allintext:foo bar",
			want: StructuredQuery{
				Raw: "allintext:foo bar", InText: []string{"foo", "bar"},
				HasDirectives: true,
			},
		},
		{
			name:  "negated term in mode lands in excluded list",
			input: "allintitle:x -y",
			want: StructuredQuery{
				Raw:             "allintitle:x -y",
				InTitle:         []string{"x"},
				ExcludedInTitle: []string{"y"},
				HasDirectives:   true, HasConstraints: true,
			},
		},
		{
			name:  "mode sticky across quoted phrases and later terms",
			input: `allintitle:a "b c" later`,
			want: StructuredQuery{
				Raw: `allintitle:a "b c" later`, InTitle: []string{"a", "b c", "later"},
				HasDirectives: true, HasConstraints: true,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertQueryEqual(t, ParseSearchQuery(c.input), c.want)
		})
	}
}

func TestParseSearchQuery_Filetypes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  StructuredQuery
	}{
		{
			name:  "filetype with free text",
			input: "filetype:pdf x",
			want: StructuredQuery{
				Raw: "filetype:pdf x", Text: "x",
				Terms:         []QueryTerm{{Text: "x"}},
				Filetypes:     []string{"pdf"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "ext alias lowercases",
			input: "ext:PDF",
			want: StructuredQuery{
				Raw: "ext:PDF", Filetypes: []string{"pdf"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "leading dot stripped",
			input: "filetype:.pdf",
			want: StructuredQuery{
				Raw: "filetype:.pdf", Filetypes: []string{"pdf"},
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "negated filetype",
			input: "-filetype:docx",
			want: StructuredQuery{
				Raw: "-filetype:docx", ExcludedFiletypes: []string{"docx"},
				HasDirectives: true, HasConstraints: true,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertQueryEqual(t, ParseSearchQuery(c.input), c.want)
		})
	}
}

func TestParseSearchQuery_Dates(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  StructuredQuery
	}{
		{
			name:  "before ISO",
			input: "before:2024-06-01",
			want: StructuredQuery{
				Raw: "before:2024-06-01", Before: "2024-06-01",
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "bare year resolves to Jan 1",
			input: "after:2024",
			want: StructuredQuery{
				Raw: "after:2024", After: "2024-01-01",
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "year-month",
			input: "after:2024-06",
			want: StructuredQuery{
				Raw: "after:2024-06", After: "2024-06-01",
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "slash separators",
			input: "after:2024/06/05",
			want: StructuredQuery{
				Raw: "after:2024/06/05", After: "2024-06-05",
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "dot separators",
			input: "after:2024.6.5",
			want: StructuredQuery{
				Raw: "after:2024.6.5", After: "2024-06-05",
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "month/day/year",
			input: "before:06/30/2024",
			want: StructuredQuery{
				Raw: "before:06/30/2024", Before: "2024-06-30",
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "day-first inference when first field exceeds 12",
			input: "after:25/12/2024",
			want: StructuredQuery{
				Raw: "after:25/12/2024", After: "2024-12-25",
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "until and since aliases",
			input: "until:2024-01-01 since:2023-01-01",
			want: StructuredQuery{
				Raw:           "until:2024-01-01 since:2023-01-01",
				After:         "2023-01-01",
				Before:        "2024-01-01",
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "invalid date degrades to plain term without directive flag",
			input: "before:someday x",
			want: StructuredQuery{
				Raw:   "before:someday x",
				Text:  "before:someday x",
				Terms: []QueryTerm{{Text: "before:someday"}, {Text: "x"}},
			},
		},
		{
			name:  "single dash before is consumed like the positive form",
			input: "-before:2024-01-01 x",
			want: StructuredQuery{
				Raw:           "-before:2024-01-01 x",
				Text:          "x",
				Terms:         []QueryTerm{{Text: "x"}},
				Before:        "2024-01-01",
				HasDirectives: true, HasConstraints: true,
			},
		},
		{
			name:  "double dash before becomes negated plain term",
			input: "--before:2024-01-01 x",
			want: StructuredQuery{
				Raw:           "--before:2024-01-01 x",
				Text:          "-before:2024-01-01 x",
				Terms:         []QueryTerm{{Text: "before:2024-01-01", Negated: true}, {Text: "x"}},
				HasDirectives: true,
			},
		},
		{
			name:  "single dash lang consumed like the positive form",
			input: "-lang:en x",
			want: StructuredQuery{
				Raw:           "-lang:en x",
				Text:          "x",
				Terms:         []QueryTerm{{Text: "x"}},
				Lang:          "en",
				HasDirectives: true,
			},
		},
		{
			name:  "double dash lang becomes negated plain term",
			input: "--lang:en x",
			want: StructuredQuery{
				Raw:           "--lang:en x",
				Text:          "-lang:en x",
				Terms:         []QueryTerm{{Text: "lang:en", Negated: true}, {Text: "x"}},
				HasDirectives: true,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertQueryEqual(t, ParseSearchQuery(c.input), c.want)
		})
	}
}

func TestParseSearchQuery_Lang(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  StructuredQuery
	}{
		{
			name:  "lang lowercased with region",
			input: "lang:en-US",
			want:  StructuredQuery{Raw: "lang:en-US", Lang: "en-us", HasDirectives: true},
		},
		{
			name:  "language alias",
			input: "language:fr",
			want:  StructuredQuery{Raw: "language:fr", Lang: "fr", HasDirectives: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertQueryEqual(t, ParseSearchQuery(c.input), c.want)
		})
	}
}

func TestParseSearchQuery_HasConstraintsDisjunction(t *testing.T) {
	// lang and intext set HasDirectives but deliberately not HasConstraints.
	assertQueryEqual(t, ParseSearchQuery("lang:en"), StructuredQuery{
		Raw: "lang:en", Lang: "en", HasDirectives: true,
	})
	assertQueryEqual(t, ParseSearchQuery("intext:x"), StructuredQuery{
		Raw: "intext:x", InText: []string{"x"}, HasDirectives: true,
	})
}

func TestParseSearchQuery_Phrases(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  StructuredQuery
	}{
		{
			name:  "quoted phrase stays quoted in text",
			input: `"exact phrase" other`,
			want: StructuredQuery{
				Raw:   `"exact phrase" other`,
				Text:  `"exact phrase" other`,
				Terms: []QueryTerm{{Text: "exact phrase", Phrase: true}, {Text: "other"}},
			},
		},
		{
			name:  "curly quotes are quotes",
			input: "“curly phrase”",
			want: StructuredQuery{
				Raw:   "“curly phrase”",
				Text:  `"curly phrase"`,
				Terms: []QueryTerm{{Text: "curly phrase", Phrase: true}},
			},
		},
		{
			name:  "empty quoted span skipped",
			input: `"" x`,
			want: StructuredQuery{
				Raw:   `"" x`,
				Text:  "x",
				Terms: []QueryTerm{{Text: "x"}},
			},
		},
		{
			name:  "whitespace-only quoted span skipped",
			input: "\"   \" x",
			want: StructuredQuery{
				Raw:   "\"   \" x",
				Text:  "x",
				Terms: []QueryTerm{{Text: "x"}},
			},
		},
		{
			name:  "standalone dash negates following quoted phrase",
			input: `-"bad phrase" x`,
			want: StructuredQuery{
				Raw:   `-"bad phrase" x`,
				Text:  `-"bad phrase" x`,
				Terms: []QueryTerm{{Text: "bad phrase", Phrase: true, Negated: true}, {Text: "x"}},
			},
		},
		{
			name:  "mid-word quote breaks word and starts phrase",
			input: `foo"bar baz"`,
			want: StructuredQuery{
				Raw:   `foo"bar baz"`,
				Text:  `foo "bar baz"`,
				Terms: []QueryTerm{{Text: "foo"}, {Text: "bar baz", Phrase: true}},
			},
		},
		{
			name:  "unclosed quote runs to end of input",
			input: `"abc`,
			want: StructuredQuery{
				Raw:   `"abc`,
				Text:  `"abc"`,
				Terms: []QueryTerm{{Text: "abc", Phrase: true}},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertQueryEqual(t, ParseSearchQuery(c.input), c.want)
		})
	}
}

func TestParseSearchQuery_Operators(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  StructuredQuery
	}{
		{
			name:  "NOT",
			input: "NOT spam x",
			want: StructuredQuery{
				Raw:           "NOT spam x",
				Text:          "-spam x",
				Terms:         []QueryTerm{{Text: "spam", Negated: true}, {Text: "x"}},
				HasDirectives: true,
			},
		},
		{
			// Only the standalone "!" token negates; "!spam" is a single bare
			// word that stays verbatim (query.ts matches tok.text === "!").
			name:  "bang-prefixed word stays verbatim",
			input: "!spam x",
			want: StructuredQuery{
				Raw:   "!spam x",
				Text:  "!spam x",
				Terms: []QueryTerm{{Text: "!spam"}, {Text: "x"}},
			},
		},
		{
			name:  "standalone bang negates next term",
			input: "! spam x",
			want: StructuredQuery{
				Raw:           "! spam x",
				Text:          "-spam x",
				Terms:         []QueryTerm{{Text: "spam", Negated: true}, {Text: "x"}},
				HasDirectives: true,
			},
		},
		{
			name:  "minus prefix keeps text",
			input: "-spam x",
			want: StructuredQuery{
				Raw:           "-spam x",
				Text:          "-spam x",
				Terms:         []QueryTerm{{Text: "spam", Negated: true}, {Text: "x"}},
				HasDirectives: true,
			},
		},
		{
			name:  "plus prefix becomes exact phrase",
			input: "+gobot x",
			want: StructuredQuery{
				Raw:           "+gobot x",
				Text:          `"gobot" x`,
				Terms:         []QueryTerm{{Text: "gobot", Phrase: true}, {Text: "x"}},
				HasDirectives: true,
			},
		},
		{
			name:  "double dash strips to single negated term",
			input: "--spam",
			want: StructuredQuery{
				Raw:           "--spam",
				Text:          "-spam",
				Terms:         []QueryTerm{{Text: "spam", Negated: true}},
				HasDirectives: true,
			},
		},
		{
			name:  "OR group",
			input: "react OR vue",
			want: StructuredQuery{
				Raw:           "react OR vue",
				Text:          "(react OR vue)",
				Terms:         []QueryTerm{{Text: "react", Group: 1}, {Text: "vue", Group: 1}},
				HasDirectives: true,
			},
		},
		{
			name:  "pipe group",
			input: "a | b",
			want: StructuredQuery{
				Raw:           "a | b",
				Text:          "(a OR b)",
				Terms:         []QueryTerm{{Text: "a", Group: 1}, {Text: "b", Group: 1}},
				HasDirectives: true,
			},
		},
		{
			name:  "double pipe group",
			input: "a || b",
			want: StructuredQuery{
				Raw:           "a || b",
				Text:          "(a OR b)",
				Terms:         []QueryTerm{{Text: "a", Group: 1}, {Text: "b", Group: 1}},
				HasDirectives: true,
			},
		},
		{
			name:  "parens dropped group kept",
			input: "(react OR vue) benchmark",
			want: StructuredQuery{
				Raw:           "(react OR vue) benchmark",
				Text:          "(react OR vue) benchmark",
				Terms:         []QueryTerm{{Text: "react", Group: 1}, {Text: "vue", Group: 1}, {Text: "benchmark"}},
				HasDirectives: true,
			},
		},
		{
			name:  "AND dropped but flag set",
			input: "a AND b",
			want: StructuredQuery{
				Raw:           "a AND b",
				Text:          "a b",
				Terms:         []QueryTerm{{Text: "a"}, {Text: "b"}},
				HasDirectives: true,
			},
		},
		{
			name:  "double ampersand dropped but flag set",
			input: "a && b",
			want: StructuredQuery{
				Raw:           "a && b",
				Text:          "a b",
				Terms:         []QueryTerm{{Text: "a"}, {Text: "b"}},
				HasDirectives: true,
			},
		},
		{
			name:  "leading OR does not adopt into a group",
			input: "OR x",
			want: StructuredQuery{
				Raw:           "OR x",
				Text:          "x",
				Terms:         []QueryTerm{{Text: "x"}},
				HasDirectives: true,
			},
		},
		{
			name:  "pure dash token yields nothing",
			input: "---",
			want: StructuredQuery{
				Raw:           "---",
				HasDirectives: true,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertQueryEqual(t, ParseSearchQuery(c.input), c.want)
		})
	}
}

func TestParseSearchQuery_UnknownColonsStayVerbatim(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"TS2345:x", "TS2345:x"},
		{`C:\path`, `C:\path`},
		{"https://example.com/x?y=1", "https://example.com/x?y=1"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			q := ParseSearchQuery(c.input)
			if len(q.Terms) != 1 {
				t.Fatalf("len(Terms) = %d, want 1", len(q.Terms))
			}
			if q.Terms[0].Text != c.want {
				t.Errorf("Terms[0].Text = %q, want %q", q.Terms[0].Text, c.want)
			}
			if q.Text != c.want {
				t.Errorf("Text = %q, want %q", q.Text, c.want)
			}
			if q.HasDirectives {
				t.Errorf("HasDirectives = true, want false for unknown colon token")
			}
		})
	}
}

func TestParseSearchQuery_EmptyAndSeparators(t *testing.T) {
	for _, input := range []string{"", "   ", " "} {
		t.Run("empty:"+strings.ReplaceAll(input, " ", "_"), func(t *testing.T) {
			assertQueryEqual(t, ParseSearchQuery(input), StructuredQuery{Raw: input})
		})
	}

	t.Run("NBSP-only", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery(" "), StructuredQuery{Raw: " "})
	})

	t.Run("NBSP as separator", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("a b"), StructuredQuery{
			Raw:   "a b",
			Text:  "a b",
			Terms: []QueryTerm{{Text: "a"}, {Text: "b"}},
		})
	})
}

func TestParseSearchQuery_TextReconstruction(t *testing.T) {
	assertQueryEqual(t, ParseSearchQuery(`x -y "z w" a OR b`), StructuredQuery{
		Raw:  `x -y "z w" a OR b`,
		Text: `x -y "z w" (a OR b)`,
		Terms: []QueryTerm{
			{Text: "x"},
			{Text: "y", Negated: true},
			{Text: "z w", Phrase: true},
			{Text: "a", Group: 1},
			{Text: "b", Group: 1},
		},
		HasDirectives: true,
	})
}

func TestParseDateValue(t *testing.T) {
	cases := []struct {
		value string
		want  string
	}{
		{"2024", "2024-01-01"},
		{"2024-6", "2024-06-01"},
		{"2024-06-05", "2024-06-05"},
		{"2024/06/05", "2024-06-05"},
		{"2024.6.5", "2024-06-05"},
		{" 2024-06-01 ", "2024-06-01"},
		{"06/30/2024", "2024-06-30"},
		{"12/25/2024", "2024-12-25"},
		{"25/12/2024", "2024-12-25"},
		{"25/13/2024", ""},
		{"6.5.2024", "2024-06-05"},
		{"1000", "1000-01-01"},
		{"9999-12-31", "9999-12-31"},
		{"2024-01-31", "2024-01-31"},
		{"12/05/2024", "2024-12-05"},
		{"13/13/2024", ""},
		{"2024-13-01", ""},
		{"0999", ""},
		{"10000", ""},
		{"2024-00-01", ""},
		{"2024-01-32", ""},
		{"2024-06-00", ""},
		{"someday", ""},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			if got := ParseDateValue(c.value); got != c.want {
				t.Errorf("ParseDateValue(%q) = %q, want %q", c.value, got, c.want)
			}
		})
	}
}

func TestFormatQuery(t *testing.T) {
	full := ParseSearchQuery(`x -y "z w" a OR b site:a.com site:b.com -site:c.com inurl:u intitle:"t v" intext:k filetype:pdf filetype:doc -filetype:exe after:2024-01-01 before:2024-06-01`)

	t.Run("zero syntax keeps terms only, flattened and unquoted", func(t *testing.T) {
		got := FormatQuery(full, QuerySyntax{})
		if got != "x z w a b" {
			t.Errorf("FormatQuery(zero) = %q, want %q", got, "x z w a b")
		}
	})

	t.Run("google syntax roundtrip", func(t *testing.T) {
		got := FormatQuery(full, GoogleQuerySyntax)
		want := `x -y "z w" (a OR b) (site:a.com OR site:b.com) -site:c.com inurl:u intitle:"t v" intext:k (filetype:pdf OR filetype:doc) -filetype:exe after:2024-01-01 before:2024-06-01`
		if got != want {
			t.Errorf("FormatQuery(google) = %q, want %q", got, want)
		}
	})

	t.Run("no phrases unquotes phrase terms", func(t *testing.T) {
		q := ParseSearchQuery(`"z w" x`)
		got := FormatQuery(q, QuerySyntax{Negation: true})
		if got != "z w x" {
			t.Errorf("FormatQuery(noPhrases) = %q, want %q", got, "z w x")
		}
	})

	t.Run("no negation drops negated terms", func(t *testing.T) {
		q := ParseSearchQuery("x -y")
		got := FormatQuery(q, QuerySyntax{Phrases: true})
		if got != "x" {
			t.Errorf("FormatQuery(noNegation) = %q, want %q", got, "x")
		}
	})

	t.Run("no or flattens groups and multi-site", func(t *testing.T) {
		q := ParseSearchQuery("a OR b site:a.com site:b.com filetype:pdf filetype:doc")
		got := FormatQuery(q, QuerySyntax{Phrases: true, Negation: true, Site: true, InURL: true, InTitle: true, InText: true, Filetype: true})
		want := "a b site:a.com site:b.com filetype:pdf filetype:doc"
		if got != want {
			t.Errorf("FormatQuery(noOr) = %q, want %q", got, want)
		}
	})

	t.Run("excluded site emitted without or-wrap regardless", func(t *testing.T) {
		q := ParseSearchQuery("-site:c.com x")
		got := FormatQuery(q, QuerySyntax{Site: true})
		if got != "x -site:c.com" {
			t.Errorf("FormatQuery(excludedSite) = %q, want %q", got, "x -site:c.com")
		}
	})

	t.Run("date range emission", func(t *testing.T) {
		q := ParseSearchQuery("x after:2024-01-01 before:2024-06-01")
		got := FormatQuery(q, QuerySyntax{DateRange: true})
		if got != "x after:2024-01-01 before:2024-06-01" {
			t.Errorf("FormatQuery(dateRange) = %q, want %q", got, "x after:2024-01-01 before:2024-06-01")
		}
	})

	t.Run("directives-only zero syntax falls back to constraint keywords in order", func(t *testing.T) {
		q := ParseSearchQuery("site:github.com intitle:hello inurl:rel intext:body filetype:pdf")
		got := FormatQuery(q, QuerySyntax{})
		if got != "github.com hello rel body pdf" {
			t.Errorf("FormatQuery(fallback) = %q, want %q", got, "github.com hello rel body pdf")
		}
	})

	t.Run("everything empty falls back to raw trimmed", func(t *testing.T) {
		q := StructuredQuery{Raw: "  raw query  "}
		if got := FormatQuery(q, QuerySyntax{}); got != "raw query" {
			t.Errorf("FormatQuery(raw fallback) = %q, want %q", got, "raw query")
		}
	})

	t.Run("empty raw returns empty", func(t *testing.T) {
		if got := FormatQuery(StructuredQuery{}, QuerySyntax{}); got != "" {
			t.Errorf("FormatQuery(empty) = %q, want empty", got)
		}
	})
}

func TestFormatScraperQuery(t *testing.T) {
	ddg := QuerySyntax{Phrases: true, Negation: true, Or: true, Site: true, InURL: true, InTitle: true, InText: true, Filetype: true}

	t.Run("no directives passthrough byte-identical", func(t *testing.T) {
		const query = "plain query with  spaces"
		parsed := ParseSearchQuery(query)
		if got := FormatScraperQuery(query, &parsed, ddg); got != query {
			t.Errorf("FormatScraperQuery = %q, want byte-identical %q", got, query)
		}
	})

	t.Run("inurl demoted after free text, bare site kept", func(t *testing.T) {
		parsed := ParseSearchQuery("site:github.com inurl:releases x")
		got := FormatScraperQuery("site:github.com inurl:releases x", &parsed, ddg)
		if got != "x releases site:github.com" {
			t.Errorf("FormatScraperQuery = %q, want %q", got, "x releases site:github.com")
		}
	})

	t.Run("path-carrying site demoted to term", func(t *testing.T) {
		parsed := ParseSearchQuery("site:github.com/anthropics cli")
		got := FormatScraperQuery("site:github.com/anthropics cli", &parsed, ddg)
		if got != "cli github.com/anthropics" {
			t.Errorf("FormatScraperQuery = %q, want %q", got, "cli github.com/anthropics")
		}
	})

	t.Run("negated site untouched", func(t *testing.T) {
		parsed := ParseSearchQuery("-site:twitter.com x")
		got := FormatScraperQuery("-site:twitter.com x", &parsed, ddg)
		if got != "x -site:twitter.com" {
			t.Errorf("FormatScraperQuery = %q, want %q", got, "x -site:twitter.com")
		}
	})

	t.Run("quoted inurl value demoted as bare term", func(t *testing.T) {
		parsed := ParseSearchQuery(`inurl:"foo bar" x`)
		got := FormatScraperQuery(`inurl:"foo bar" x`, &parsed, ddg)
		if got != "x foo bar" {
			t.Errorf("FormatScraperQuery = %q, want %q", got, "x foo bar")
		}
	})

	t.Run("grouped path site demoted, bare site kept as directive", func(t *testing.T) {
		parsed := ParseSearchQuery("site:a.com/x OR site:b.com")
		got := FormatScraperQuery("site:a.com/x OR site:b.com", &parsed, ddg)
		if got != "a.com/x site:b.com" {
			t.Errorf("FormatScraperQuery = %q, want %q", got, "a.com/x site:b.com")
		}
	})

	t.Run("nil parsed parses internally", func(t *testing.T) {
		const query = "site:github.com inurl:releases x"
		if got := FormatScraperQuery(query, nil, ddg); got != "x releases site:github.com" {
			t.Errorf("FormatScraperQuery(nil parsed) = %q, want %q", got, "x releases site:github.com")
		}
	})
}

func TestMatchesSite(t *testing.T) {
	cases := []struct {
		name string
		url  string
		site string
		want bool
	}{
		{"exact host", "https://github.com/x", "github.com", true},
		{"subdomain", "https://sub.github.com/x", "github.com", true},
		{"uppercase url host", "https://GITHUB.com/x", "github.com", true},
		{"port stripped", "https://github.com:8080/x", "github.com", true},
		{"path site matches prefix", "https://github.com/anthropics/claude", "github.com/anthropics", true},
		{"path site wrong path", "https://github.com/other/x", "github.com/anthropics", false},
		{"path site case-insensitive path", "https://github.com/Anthropics/claude", "github.com/anthropics", true},
		{"different host", "https://example.com/x", "github.com", false},
		{"unparsable url", "http://[::1]:namedport", "github.com", false},
		// JS new URL throws on scheme-less input; Go parses it with an empty
		// host, so the empty-host rule — not parse failure — must reject it.
		{"scheme-less url never matches", "example.com/x", "example.com", false},
		{"host is not a subdomain suffix", "https://notgithub.com/x", "github.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatchesSite(c.url, c.site); got != c.want {
				t.Errorf("MatchesSite(%q, %q) = %v, want %v", c.url, c.site, got, c.want)
			}
		})
	}
}

func TestApplyQueryConstraints(t *testing.T) {
	t.Run("empty input yields empty without evaluating dimensions", func(t *testing.T) {
		res := ApplyQueryConstraints(nil, ParseSearchQuery("site:github.com"))
		if len(res.Sources) != 0 {
			t.Errorf("len(Sources) = %d, want 0", len(res.Sources))
		}
		if len(res.Dropped) != 0 {
			t.Errorf("len(Dropped) = %d, want 0", len(res.Dropped))
		}
	})

	github1 := SearchSource{Title: "a", URL: "https://github.com/a"}
	github2 := SearchSource{Title: "b", URL: "https://github.com/b"}
	example := SearchSource{Title: "c", URL: "https://example.com/c"}

	t.Run("sites filter keeps matching only", func(t *testing.T) {
		res := ApplyQueryConstraints([]SearchSource{github1, example, github2}, ParseSearchQuery("site:github.com"))
		if len(res.Sources) != 2 || res.Sources[0].URL != "https://github.com/a" || res.Sources[1].URL != "https://github.com/b" {
			t.Errorf("Sources = %v, want the two github sources in order", res.Sources)
		}
		if len(res.Dropped) != 0 {
			t.Errorf("Dropped = %v, want none", res.Dropped)
		}
	})

	t.Run("all-fail keeps everything and reports the exact label", func(t *testing.T) {
		res := ApplyQueryConstraints([]SearchSource{example}, ParseSearchQuery("site:github.com"))
		if len(res.Sources) != 1 || res.Sources[0].URL != "https://example.com/c" {
			t.Errorf("Sources = %v, want the surviving example source", res.Sources)
		}
		if len(res.Dropped) != 1 || res.Dropped[0] != "site:github.com" {
			t.Errorf("Dropped = %v, want [site:github.com]", res.Dropped)
		}
	})

	t.Run("second dimension evaluated on first's survivors", func(t *testing.T) {
		// inurl:zzz matches nothing (inurl is a substring test on the whole
		// URL, so a value like "c" would spuriously match every .com URL).
		res := ApplyQueryConstraints([]SearchSource{github1, example}, ParseSearchQuery("site:github.com inurl:zzz"))
		if len(res.Sources) != 1 || res.Sources[0].URL != "https://github.com/a" {
			t.Errorf("Sources = %v, want only the github survivor", res.Sources)
		}
		if len(res.Dropped) != 1 || res.Dropped[0] != "inurl:zzz" {
			t.Errorf("Dropped = %v, want [inurl:zzz]", res.Dropped)
		}
	})

	t.Run("excluded sites dimension", func(t *testing.T) {
		a := SearchSource{Title: "a", URL: "https://a.com/x"}
		b := SearchSource{Title: "b", URL: "https://b.com/x"}
		res := ApplyQueryConstraints([]SearchSource{a, b}, ParseSearchQuery("-site:b.com"))
		if len(res.Sources) != 1 || res.Sources[0].URL != "https://a.com/x" {
			t.Errorf("Sources = %v, want only a.com", res.Sources)
		}
	})

	t.Run("inurl all-of and excluded any-of", func(t *testing.T) {
		both := SearchSource{Title: "x", URL: "https://x.com/releases/go"}
		one := SearchSource{Title: "y", URL: "https://y.com/releases"}
		res := ApplyQueryConstraints([]SearchSource{both, one}, ParseSearchQuery("inurl:releases inurl:go"))
		if len(res.Sources) != 1 || res.Sources[0].URL != "https://x.com/releases/go" {
			t.Errorf("Sources = %v, want only the all-of match", res.Sources)
		}
		res = ApplyQueryConstraints([]SearchSource{both, one}, ParseSearchQuery("-inurl:go"))
		if len(res.Sources) != 1 || res.Sources[0].URL != "https://y.com/releases" {
			t.Errorf("Sources = %v, want only the exclusion survivor", res.Sources)
		}
	})

	t.Run("intitle lowercase substring", func(t *testing.T) {
		src := SearchSource{Title: "Learning Go Today", URL: "https://x.com"}
		other := SearchSource{Title: "Rust Guide", URL: "https://y.com"}
		res := ApplyQueryConstraints([]SearchSource{src, other}, ParseSearchQuery("intitle:go"))
		if len(res.Sources) != 1 || res.Sources[0].Title != "Learning Go Today" {
			t.Errorf("Sources = %v, want the case-insensitive title match", res.Sources)
		}
	})

	t.Run("filetype any-of via URL suffix", func(t *testing.T) {
		pdf := SearchSource{Title: "p", URL: "https://x.com/paper.pdf"}
		doc := SearchSource{Title: "d", URL: "https://x.com/paper.doc"}
		web := SearchSource{Title: "w", URL: "https://x.com/page.html"}
		res := ApplyQueryConstraints([]SearchSource{web, pdf, doc}, ParseSearchQuery("filetype:pdf filetype:doc"))
		if len(res.Sources) != 2 || res.Sources[0].URL != "https://x.com/paper.pdf" || res.Sources[1].URL != "https://x.com/paper.doc" {
			t.Errorf("Sources = %v, want pdf and doc", res.Sources)
		}
	})

	t.Run("filetype on scheme-less URL does not match", func(t *testing.T) {
		src := SearchSource{Title: "p", URL: "example.com/paper.pdf"}
		res := ApplyQueryConstraints([]SearchSource{src}, ParseSearchQuery("filetype:pdf"))
		if len(res.Sources) != 1 {
			t.Fatalf("len(Sources) = %d, want 1 (kept via relaxation)", len(res.Sources))
		}
		if len(res.Dropped) != 1 || res.Dropped[0] != "filetype:pdf" {
			t.Errorf("Dropped = %v, want [filetype:pdf] — scheme-less URL must not suffix-match", res.Dropped)
		}
	})

	t.Run("date bounds inclusive after exclusive before, undated passes", func(t *testing.T) {
		q := ParseSearchQuery("after:2024-06-01")
		onBound := SearchSource{Title: "b", URL: "https://b.com", PublishedDate: "2024-06-01"}
		before := SearchSource{Title: "o", URL: "https://o.com", PublishedDate: "2024-05-31"}
		undated := SearchSource{Title: "u", URL: "https://u.com"}
		res := ApplyQueryConstraints([]SearchSource{before, onBound, undated}, q)
		if len(res.Sources) != 2 || res.Sources[0].Title != "b" || res.Sources[1].Title != "u" {
			t.Errorf("Sources = %v, want on-bound and undated", res.Sources)
		}

		q = ParseSearchQuery("before:2024-06-01")
		onBoundBefore := SearchSource{Title: "b", URL: "https://b.com", PublishedDate: "2024-06-01"}
		inside := SearchSource{Title: "i", URL: "https://i.com", PublishedDate: "2024-05-31"}
		res = ApplyQueryConstraints([]SearchSource{onBoundBefore, inside}, q)
		if len(res.Sources) != 1 || res.Sources[0].Title != "i" {
			t.Errorf("Sources = %v, want only the strictly-before source", res.Sources)
		}
	})

	t.Run("ageSeconds beats publishedDate", func(t *testing.T) {
		src := SearchSource{Title: "fresh", URL: "https://f.com", PublishedDate: "2020-01-01", AgeSeconds: 60}
		res := ApplyQueryConstraints([]SearchSource{src}, ParseSearchQuery("after:2024-01-01"))
		if len(res.Sources) != 1 {
			t.Fatalf("len(Sources) = %d, want 1 (age wins, bound passes)", len(res.Sources))
		}
		if len(res.Dropped) != 0 {
			t.Errorf("Dropped = %v, want none", res.Dropped)
		}
	})

	t.Run("relative publishedDate honored", func(t *testing.T) {
		recent := SearchSource{Title: "r", URL: "https://r.com", PublishedDate: "3 days ago"}
		res := ApplyQueryConstraints([]SearchSource{recent}, ParseSearchQuery("after:2020-01-01"))
		if len(res.Sources) != 1 || len(res.Dropped) != 0 {
			t.Fatalf("Sources/Dropped = %v/%v, want 1 kept and no drops for a relative date after the bound", res.Sources, res.Dropped)
		}
		res = ApplyQueryConstraints([]SearchSource{recent}, ParseSearchQuery("before:2020-01-01"))
		if len(res.Sources) != 1 {
			t.Fatalf("len(Sources) = %d, want 1 (kept via relaxation after failing the before bound)", len(res.Sources))
		}
		if len(res.Dropped) != 1 || res.Dropped[0] != "before:2020-01-01" {
			t.Errorf("Dropped = %v, want [before:2020-01-01]", res.Dropped)
		}
	})

	t.Run("multi-value labels exact", func(t *testing.T) {
		res := ApplyQueryConstraints([]SearchSource{example}, ParseSearchQuery("site:a.com OR site:b.com"))
		if len(res.Dropped) != 1 || res.Dropped[0] != "site:a.com OR site:b.com" {
			t.Errorf("Dropped = %v, want [site:a.com OR site:b.com]", res.Dropped)
		}
	})

	t.Run("input slice not mutated", func(t *testing.T) {
		original := []SearchSource{github1, example, github2}
		ApplyQueryConstraints(original, ParseSearchQuery("site:github.com"))
		if len(original) != 3 || original[0].URL != "https://github.com/a" || original[1].URL != "https://example.com/c" || original[2].URL != "https://github.com/b" {
			t.Errorf("input slice mutated: %v", original)
		}
	})
}

func TestMatchesQueryConstraints(t *testing.T) {
	src := SearchSource{Title: "Go", URL: "https://github.com/x"}
	if !MatchesQueryConstraints(src, ParseSearchQuery("site:github.com intitle:go")) {
		t.Error("all dimensions pass: want true")
	}
	if MatchesQueryConstraints(src, ParseSearchQuery("site:example.com")) {
		t.Error("one violated dimension: want false")
	}
	if !MatchesQueryConstraints(src, ParseSearchQuery("plain query")) {
		t.Error("no dimensions: want true")
	}
}

func TestParseSearchQuery_CoverageEdges(t *testing.T) {
	t.Run("internal close paren before trailing one survives depth scan", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("a)b) x"), StructuredQuery{
			Raw:   "a)b) x",
			Text:  "a)b x",
			Terms: []QueryTerm{{Text: "a)b"}, {Text: "x"}},
		})
	})

	t.Run("valueless site does not adopt a directive token", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("site: inurl:x"), StructuredQuery{
			Raw:           "site: inurl:x",
			InURL:         []string{"x"},
			HasDirectives: true, HasConstraints: true,
		})
	})

	t.Run("double dash before directive re-checks as excluded constraint", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("--site:x.com"), StructuredQuery{
			Raw: "--site:x.com", ExcludedSites: []string{"x.com"},
			HasDirectives: true, HasConstraints: true,
		})
	})
}

func TestFormatQuery_ExclusionsEmission(t *testing.T) {
	q := ParseSearchQuery("-inurl:u -intitle:t -intext:k -filetype:pdf -site:s.com")
	got := FormatQuery(q, GoogleQuerySyntax)
	if got != "-site:s.com -inurl:u -intitle:t -intext:k -filetype:pdf" {
		t.Errorf("FormatQuery(exclusions) = %q, want %q", got, "-site:s.com -inurl:u -intitle:t -intext:k -filetype:pdf")
	}
}

func TestFormatQuery_SingleRenderableGroupMemberBare(t *testing.T) {
	// With negation syntax off, the negated member drops and the group's
	// single surviving member renders bare (no parens).
	q := ParseSearchQuery("-x OR y")
	got := FormatQuery(q, QuerySyntax{Or: true})
	if got != "y" {
		t.Errorf("FormatQuery(single member) = %q, want %q", got, "y")
	}
}

func TestApplyQueryConstraints_ExcludedDimensions(t *testing.T) {
	spamTitle := SearchSource{Title: "buy spam now", URL: "https://a.com/x"}
	cleanTitle := SearchSource{Title: "real news", URL: "https://b.com/y"}

	t.Run("excluded intitle", func(t *testing.T) {
		res := ApplyQueryConstraints([]SearchSource{spamTitle, cleanTitle}, ParseSearchQuery("-intitle:spam"))
		if len(res.Sources) != 1 || res.Sources[0].Title != "real news" {
			t.Errorf("Sources = %v, want only the clean title", res.Sources)
		}
	})

	t.Run("excluded filetype", func(t *testing.T) {
		pdf := SearchSource{Title: "p", URL: "https://a.com/p.pdf"}
		html := SearchSource{Title: "h", URL: "https://b.com/p.html"}
		res := ApplyQueryConstraints([]SearchSource{pdf, html}, ParseSearchQuery("-filetype:pdf"))
		if len(res.Sources) != 1 || res.Sources[0].URL != "https://b.com/p.html" {
			t.Errorf("Sources = %v, want only the html source", res.Sources)
		}
		res = ApplyQueryConstraints([]SearchSource{pdf}, ParseSearchQuery("-filetype:pdf"))
		if len(res.Sources) != 1 || len(res.Dropped) != 1 || res.Dropped[0] != "-filetype:pdf" {
			t.Errorf("all-fail excluded filetype: Sources/Dropped = %v/%v, want kept + [-filetype:pdf]", res.Sources, res.Dropped)
		}
	})
}

func TestApplyQueryConstraints_UnresolvableDates(t *testing.T) {
	// Both relative-zero and unparseable published dates resolve as undated,
	// which passes date bounds rather than violating them.
	sources := []SearchSource{
		{Title: "zero", URL: "https://z.com", PublishedDate: "0 days ago"},
		{Title: "loose", URL: "https://l.com", PublishedDate: "January 5, 2024"},
	}
	res := ApplyQueryConstraints(sources, ParseSearchQuery("after:2024-06-01"))
	if len(res.Sources) != 2 || len(res.Dropped) != 0 {
		t.Errorf("Sources/Dropped = %v/%v, want both kept as undated", res.Sources, res.Dropped)
	}
}

func TestFormatQuery_AllGroupMembersDropped(t *testing.T) {
	// Every group member negated with negation syntax off: the group
	// contributes nothing; only the plain term survives.
	q := ParseSearchQuery("keep -x OR -y")
	if got := FormatQuery(q, QuerySyntax{Or: true}); got != "keep" {
		t.Errorf("FormatQuery(all dropped) = %q, want %q", got, "keep")
	}
	// With nothing else left, the never-empty guarantee falls back to raw.
	q = ParseSearchQuery("-x OR -y")
	if got := FormatQuery(q, QuerySyntax{Or: true}); got != "-x OR -y" {
		t.Errorf("FormatQuery(raw fallback) = %q, want %q", got, "-x OR -y")
	}
}

// TestParseSearchQuery_MutantKills pins behaviors that survived the first
// mutation pass: each case kills at least one previously-escaped mutant.
func TestParseSearchQuery_MutantKills(t *testing.T) {
	t.Run("char directly after closing quote is not skipped", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery(`"ab"cd`), StructuredQuery{
			Raw:   `"ab"cd`,
			Text:  `"ab" cd`,
			Terms: []QueryTerm{{Text: "ab", Phrase: true}, {Text: "cd"}},
		})
	})

	t.Run("word loop continues after a swallowed quoted value", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery(`intitle:"a b"cd`), StructuredQuery{
			Raw:           `intitle:"a b"cd`,
			InTitle:       []string{"a bcd"},
			HasDirectives: true, HasConstraints: true,
		})
	})

	t.Run("quoted directive value with trailing paren stays whole", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery(`intitle:"a)"`), StructuredQuery{
			Raw:           `intitle:"a)"`,
			InTitle:       []string{"a)"},
			HasDirectives: true, HasConstraints: true,
		})
	})

	t.Run("internal close-then-open paren keeps trailing close attached", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("a)(b) x"), StructuredQuery{
			Raw:   "a)(b) x",
			Text:  "a)(b x",
			Terms: []QueryTerm{{Text: "a)(b"}, {Text: "x"}},
		})
	})

	t.Run("dash quoted at end of input still negates", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery(`x -"a"`), StructuredQuery{
			Raw:   `x -"a"`,
			Text:  `x -"a"`,
			Terms: []QueryTerm{{Text: "x"}, {Text: "a", Phrase: true, Negated: true}},
		})
	})

	t.Run("quoted reserved token adopted as directive value", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery(`site: "OR"`), StructuredQuery{
			Raw:           `site: "OR"`,
			Sites:         []string{"or"},
			HasDirectives: true, HasConstraints: true,
		})
	})

	t.Run("valueless directive alone still flags HasDirectives", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("site:"), StructuredQuery{
			Raw:           "site:",
			HasDirectives: true,
		})
	})

	t.Run("negateNext consumed by directive, not carried to next term", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("NOT site:x.com y"), StructuredQuery{
			Raw:           "NOT site:x.com y",
			Text:          "y",
			Terms:         []QueryTerm{{Text: "y"}},
			ExcludedSites: []string{"x.com"},
			HasDirectives: true, HasConstraints: true,
		})
	})

	t.Run("directive between OR-separated terms does not join a group", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("a OR site:x.com y"), StructuredQuery{
			Raw:           "a OR site:x.com y",
			Text:          "a y",
			Terms:         []QueryTerm{{Text: "a"}, {Text: "y"}},
			Sites:         []string{"x.com"},
			HasDirectives: true, HasConstraints: true,
		})
	})

	t.Run("pure dash arms negation into the following term", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("--- x"), StructuredQuery{
			Raw:           "--- x",
			Text:          "-x",
			Terms:         []QueryTerm{{Text: "x", Negated: true}},
			HasDirectives: true,
		})
	})

	t.Run("valueless allintitle flags HasDirectives", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("allintitle:"), StructuredQuery{
			Raw:           "allintitle:",
			HasDirectives: true,
		})
	})

	t.Run("valueless before flags HasDirectives without a term", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("before:"), StructuredQuery{
			Raw:           "before:",
			HasDirectives: true,
		})
	})

	t.Run("plus before quoted does not negate", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery(`+"a" x`), StructuredQuery{
			Raw:   `+"a" x`,
			Text:  `"a" x`,
			Terms: []QueryTerm{{Text: "a", Phrase: true}, {Text: "x"}},
		})
	})

	t.Run("double dash after becomes negated plain term", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("--after:2024-01-01 x"), StructuredQuery{
			Raw:           "--after:2024-01-01 x",
			Text:          "-after:2024-01-01 x",
			Terms:         []QueryTerm{{Text: "after:2024-01-01", Negated: true}, {Text: "x"}},
			HasDirectives: true,
		})
	})

	t.Run("excluded inurl and intitle count as constraints", func(t *testing.T) {
		assertQueryEqual(t, ParseSearchQuery("-inurl:x"), StructuredQuery{
			Raw: "-inurl:x", ExcludedInURL: []string{"x"},
			HasDirectives: true, HasConstraints: true,
		})
		assertQueryEqual(t, ParseSearchQuery("-intitle:x"), StructuredQuery{
			Raw: "-intitle:x", ExcludedInTitle: []string{"x"},
			HasDirectives: true, HasConstraints: true,
		})
	})

	t.Run("double dash valueless site keeps every list empty", func(t *testing.T) {
		// --site: reaches pushConstraint with an empty value only via the
		// dash-strip re-check (query.ts pushes nothing and still flags
		// hasDirectives); normalizeSite("") would subsume the site case alone.
		assertQueryEqual(t, ParseSearchQuery("--site:"), StructuredQuery{
			Raw:           "--site:",
			HasDirectives: true,
		})
	})

	t.Run("double dash valueless inurl keeps the exclusion list empty", func(t *testing.T) {
		// Unlike site:, the inurl family has no per-field empty guard, so the
		// empty-value guard in pushConstraint is the only thing standing
		// between "--inurl:" and a phantom "" exclusion constraint.
		assertQueryEqual(t, ParseSearchQuery("--inurl:"), StructuredQuery{
			Raw:           "--inurl:",
			HasDirectives: true,
		})
	})

	t.Run("trailing standalone hyphen neither panics nor negates", func(t *testing.T) {
		// tokens ["x","-"]: the quoted lookahead must respect the end of the
		// token slice — "-" as the final token is a plain no-op.
		assertQueryEqual(t, ParseSearchQuery("x -"), StructuredQuery{
			Raw:   "x -",
			Text:  "x",
			Terms: []QueryTerm{{Text: "x"}},
		})
	})

	t.Run("after with day zero degrades to a plain term", func(t *testing.T) {
		// The date regex accepts 06-00, so isoDate's day<1 check is the live
		// rejection (TS isoDate:243): the directive must not survive as a
		// date bound.
		assertQueryEqual(t, ParseSearchQuery("after:2024-06-00"), StructuredQuery{
			Raw:   "after:2024-06-00",
			Text:  "after:2024-06-00",
			Terms: []QueryTerm{{Text: "after:2024-06-00"}},
		})
	})
}

func TestApplyQueryConstraints_DroppedLabels(t *testing.T) {
	src := SearchSource{Title: "T", URL: "https://example.com/page.html", PublishedDate: "2023-05-05"}
	cases := []struct {
		name  string
		query string
		want  string
		src   SearchSource
	}{
		{"excluded site", "-site:example.com", "-site:example.com", src},
		{"intitle", "intitle:zzz", "intitle:zzz", src},
		{"excluded intitle", "-intitle:zzz", "-intitle:zzz", SearchSource{Title: "zzz here", URL: "https://example.com/x"}},
		{"excluded inurl", "-inurl:zzz", "-inurl:zzz", SearchSource{Title: "t", URL: "https://example.com/zzz"}},
		{"filetype single", "filetype:zzz", "filetype:zzz", src},
		{"filetype multi joined with OR", "filetype:pdf filetype:doc", "filetype:pdf OR filetype:doc", src},
		{"date bounds joined", "after:2024-01-01 before:2024-06-01", "after:2024-01-01 before:2024-06-01", src},
		{"multi excluded sites space-joined", "-site:a.com -site:b.com", "-site:a.com -site:b.com", SearchSource{Title: "t", URL: "https://a.com/x"}},
		{"multi excluded inurl space-joined", "-inurl:zzz -inurl:yyy", "-inurl:zzz -inurl:yyy", SearchSource{Title: "t", URL: "https://example.com/zzz"}},
		{"multi intitle space-joined", "intitle:zzz intitle:yyy", "intitle:zzz intitle:yyy", src},
		{"multi excluded intitle space-joined", "-intitle:zzz -intitle:yyy", "-intitle:zzz -intitle:yyy", SearchSource{Title: "zzz yyy", URL: "https://example.com/x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := ApplyQueryConstraints([]SearchSource{c.src}, ParseSearchQuery(c.query))
			if len(res.Sources) != 1 {
				t.Fatalf("len(Sources) = %d, want 1 (relaxed, never emptied)", len(res.Sources))
			}
			if len(res.Dropped) != 1 || res.Dropped[0] != c.want {
				t.Errorf("Dropped = %v, want [%s]", res.Dropped, c.want)
			}
		})
	}
}

func TestApplyQueryConstraints_SourceTimeMutants(t *testing.T) {
	utcDate := func(days int) string {
		// Wall-clock fixture with multi-day margins: sourceTime computes ages
		// against the system clock. REAL-TIME.
		return time.Now().UTC().AddDate(0, 0, days).Format("2006-01-02")
	}
	srcDated := func(published string, age int64) SearchSource {
		return SearchSource{Title: "t", URL: "https://t.com", PublishedDate: published, AgeSeconds: age}
	}

	t.Run("future-dated source violates a before bound", func(t *testing.T) {
		src := srcDated("2020-01-01", -400*86400) // 400 days in the future
		res := ApplyQueryConstraints([]SearchSource{src}, ParseSearchQuery("before:"+utcDate(1)))
		if len(res.Sources) != 1 {
			t.Fatalf("len(Sources) = %d, want 1 (relaxed)", len(res.Sources))
		}
		if len(res.Dropped) != 1 || res.Dropped[0] != "before:"+utcDate(1) {
			t.Errorf("Dropped = %v, want [before:%s] — the future-dated source must violate", res.Dropped, utcDate(1))
		}
	})

	t.Run("relative days ago passes a tomorrow before-bound", func(t *testing.T) {
		src := srcDated("3 days ago", 0)
		res := ApplyQueryConstraints([]SearchSource{src}, ParseSearchQuery("before:"+utcDate(1)))
		if len(res.Sources) != 1 || len(res.Dropped) != 0 {
			t.Errorf("Sources/Dropped = %v/%v, want kept with no drop (3 days ago is before tomorrow)", res.Sources, res.Dropped)
		}
	})

	t.Run("zero-relative resolves undated and passes", func(t *testing.T) {
		src := srcDated("0 days ago", 0)
		res := ApplyQueryConstraints([]SearchSource{src}, ParseSearchQuery("before:"+utcDate(0)))
		if len(res.Sources) != 1 || len(res.Dropped) != 0 {
			t.Errorf("Sources/Dropped = %v/%v, want kept with no drop (0 days ago = now, undated by the >0 guard)", res.Sources, res.Dropped)
		}
	})

	t.Run("3 days ago violates an after bound two days back", func(t *testing.T) {
		src := srcDated("3 days ago", 0)
		res := ApplyQueryConstraints([]SearchSource{src}, ParseSearchQuery("after:"+utcDate(-2)))
		if len(res.Sources) != 1 || len(res.Dropped) != 1 || res.Dropped[0] != "after:"+utcDate(-2) {
			t.Errorf("Sources/Dropped = %v/%v, want kept + [after:%s] (3 days ago is older than 2 days ago)", res.Sources, res.Dropped, utcDate(-2))
		}
	})

	relUnitCases := []struct {
		published string
		seconds   int64 // exact unit×count age the unit map must resolve to
	}{
		{"3 days ago", 3 * 86400},
		{"3 d ago", 3 * 86400},
		{"2 weeks ago", 2 * 604800},
		{"2 w ago", 2 * 604800},
		{"2 months ago", 2 * 2592000},
		{"2 mo ago", 2 * 2592000},
		{"2 years ago", 2 * 31536000},
		{"2 y ago", 2 * 31536000},
		{"2 yr ago", 2 * 31536000},
		{"5 m ago", 300},
		{"3 min ago", 180},
		{"2 minute ago", 120},
		{"4 h ago", 14400},
		{"6 hr ago", 21600},
		{"1 hour ago", 3600},
	}
	for _, rc := range relUnitCases {
		t.Run("relative unit entry present: "+rc.published, func(t *testing.T) {
			src := srcDated(rc.published, 0)
			// Sub-day ages are invisible to day-granularity after: bounds, so
			// the unit-map entry is measured directly. ±7s keeps a ±1s unit
			// tweak inside the equivalence margin (a count up to 6 amplifies
			// it to 6s) while swapping unit families (60↔3600) fails hard.
			pt, ok := sourceTime(src)
			if !ok {
				t.Fatalf("sourceTime(%q) = undated, want dated via the unit map", rc.published)
			}
			if age := time.Since(pt).Seconds(); age < float64(rc.seconds)-7 || age > float64(rc.seconds)+7 {
				t.Errorf("sourceTime(%q) age = %.1fs, want within 7s of %d", rc.published, age, rc.seconds)
			}
			// Day-granular pipeline pin: a bound inside the correct age is
			// violated by the dated source, an undated one would pass.
			if boundDays := rc.seconds / 86400 / 2; boundDays > 0 {
				bound := "after:" + utcDate(-int(boundDays))
				res := ApplyQueryConstraints([]SearchSource{src}, ParseSearchQuery(bound))
				if len(res.Sources) != 1 || len(res.Dropped) != 1 || res.Dropped[0] != bound {
					t.Errorf("%s: Sources/Dropped = %v/%v, want kept + [%s]", rc.published, res.Sources, res.Dropped, bound)
				}
			}
		})
	}

	t.Run("multi-digit relative count scales by the unit", func(t *testing.T) {
		src := srcDated("40 days ago", 0)
		res := ApplyQueryConstraints([]SearchSource{src}, ParseSearchQuery("after:"+utcDate(-30)))
		if len(res.Sources) != 1 || len(res.Dropped) != 1 || res.Dropped[0] != "after:"+utcDate(-30) {
			t.Errorf("Sources/Dropped = %v/%v, want kept + [after:%s] (40 days ago is older than 30 days ago)", res.Sources, res.Dropped, utcDate(-30))
		}
	})

	// 50 days pins the decimal accumulation: n*11 → 55 (older than 52 → drops),
	// correct 50 is newer than 52 → passes.
	t.Run("decimal accumulation upper edge", func(t *testing.T) {
		src := srcDated("50 days ago", 0)
		res := ApplyQueryConstraints([]SearchSource{src}, ParseSearchQuery("after:"+utcDate(-52)))
		if len(res.Sources) != 1 || len(res.Dropped) != 0 {
			t.Errorf("Sources/Dropped = %v/%v, want kept with no drop (50 days ago is newer than 52 days ago)", res.Sources, res.Dropped)
		}
	})

	// n*9 → 45 (newer than 48 → passes); correct 50 is older than 48 → drops.
	t.Run("decimal accumulation lower edge", func(t *testing.T) {
		src := srcDated("50 days ago", 0)
		res := ApplyQueryConstraints([]SearchSource{src}, ParseSearchQuery("after:"+utcDate(-48)))
		if len(res.Sources) != 1 || len(res.Dropped) != 1 || res.Dropped[0] != "after:"+utcDate(-48) {
			t.Errorf("Sources/Dropped = %v/%v, want kept + [after:%s] (50 days ago is older than 48 days ago)", res.Sources, res.Dropped, utcDate(-48))
		}
	})
}

func TestApplyQueryConstraints_UnparseableBoundIsUnset(t *testing.T) {
	// A hand-built StructuredQuery can carry an invalid After/Before; both
	// must behave as unset bounds, never as zero-time bounds.
	src := SearchSource{Title: "t", URL: "https://t.com", PublishedDate: "2020-01-01"}
	for _, field := range []string{"After", "Before"} {
		q := ParseSearchQuery("x")
		if field == "After" {
			q.After = "bogus"
		} else {
			q.Before = "bogus"
		}
		res := ApplyQueryConstraints([]SearchSource{src}, q)
		if len(res.Sources) != 1 || len(res.Dropped) != 0 {
			t.Errorf("%s=bogus: Sources/Dropped = %v/%v, want kept with no drop (bound unset)", field, res.Sources, res.Dropped)
		}
	}
}
