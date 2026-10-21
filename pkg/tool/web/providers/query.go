package providers

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Structured query parsing, ported from omp
// packages/coding-agent/src/web/search/query.ts.
//
// Agents habitually embed Google-style directives in search queries — site:,
// before:/after:, inurl:, filetype:, quoted phrases, OR groups, -exclusions —
// regardless of whether the backing engine parses them. This layer turns a
// raw query into a StructuredQuery so each provider can map constraints onto
// native API parameters, rebuild a query string containing only the syntax
// the target engine understands (FormatQuery), and post-filter returned
// sources leniently (ApplyQueryConstraints): a constraint dimension that
// would eliminate every result is dropped and reported rather than returning
// nothing.

// QueryTerm is one free-text token of the query (everything that is not a
// recognized directive).
type QueryTerm struct {
	// Text is the term text without quotes or operator prefixes.
	Text string
	// Phrase marks a quoted exact phrase ("like this") or verbatim-required
	// (+term).
	Phrase bool
	// Negated marks exclusion via -term or NOT term.
	Negated bool
	// Group is the OR-group id; terms sharing an id are alternatives (a OR b).
	// Zero means no group (implicitly AND-ed). Groups are always contiguous
	// runs in StructuredQuery.Terms.
	Group int
}

// StructuredQuery is a raw query decomposed into free text plus every
// recognized constraint. List fields may be nil when empty; emptiness is
// len == 0. Values are stored as typed by the user except for normalization
// noted per field.
type StructuredQuery struct {
	// Raw is the original query string, verbatim.
	Raw string
	// Text is the free-text remainder with all recognized directives removed;
	// phrases stay quoted, exclusions keep -, OR groups keep OR. Empty when
	// the query was directives only — use FormatQuery for a never-empty
	// engine query.
	Text  string
	Terms []QueryTerm
	// Sites holds site:/domain:/host: includes — any-of. Lowercased, scheme
	// stripped, may carry a path (github.com/anthropics).
	Sites         []string
	ExcludedSites []string
	// InURL holds inurl:/url: substrings — all must appear in the URL.
	InURL           []string
	ExcludedInURL   []string
	InTitle         []string
	ExcludedInTitle []string
	// InText holds intext:/inbody:/inanchor: body substrings. Not
	// post-filterable (snippets are partial); query-building only.
	InText            []string
	ExcludedInText    []string
	Filetypes         []string
	ExcludedFiletypes []string
	// After is the inclusive lower publish-date bound from after:/since:,
	// ISO YYYY-MM-DD; "" = absent.
	After string
	// Before is the exclusive upper publish-date bound from before:/until:.
	Before string
	// Lang is the language code from lang:/language:, lowercased (en, en-us).
	Lang string
	// HasDirectives is true when any directive or boolean operator was
	// recognized.
	HasDirectives bool
	// HasConstraints is true when any post-filterable constraint is set
	// (sites, url/title terms, filetypes, date bounds — deliberately not
	// inText, not lang).
	HasConstraints bool
}

// QuerySyntax describes the query-syntax capabilities of a target engine,
// used by FormatQuery to decide which parsed features are re-emitted as
// query text. The zero value produces plain keywords suitable for
// natural-language APIs.
type QuerySyntax struct {
	Phrases   bool
	Negation  bool
	Or        bool
	Site      bool
	InURL     bool
	InTitle   bool
	InText    bool
	Filetype  bool
	DateRange bool
}

// GoogleQuerySyntax is the full Google-style operator set: engines that parse
// the classic operators (Google, Startpage, Ecosia, Brave, Kagi, Mojeek,
// SearXNG…).
var GoogleQuerySyntax = QuerySyntax{
	Phrases:   true,
	Negation:  true,
	Or:        true,
	Site:      true,
	InURL:     true,
	InTitle:   true,
	InText:    true,
	Filetype:  true,
	DateRange: true,
}

var directivePattern = regexp.MustCompile(`(?i)^([+-]?)([a-z][a-z-]*):(.*)$`)

type allModeField string

const (
	allModeInTitle allModeField = "inTitle"
	allModeInURL   allModeField = "inUrl"
	allModeInText  allModeField = "inText"
)

type rawToken struct {
	text string
	// quoted marks a token that was entirely a quoted phrase.
	quoted bool
	// quotedValue marks a directive value that was quoted (intitle:"a b").
	quotedValue bool
}

func isQuote(r rune) bool {
	return r == '"' || r == '“' || r == '”'
}

// tokenize splits a raw query into whitespace-delimited tokens, honoring
// quoted spans and standalone parens. It walks runes (JS walks UTF-16 units;
// equivalent for real queries) and uses unicode.IsSpace so pasted NBSP and
// friends act as separators, mirroring the TS /\s/ test.
func tokenize(raw string) []rawToken {
	var tokens []rawToken
	runes := []rune(raw)
	n := len(runes)
	i := 0
	for i < n {
		ch := runes[i]
		if unicode.IsSpace(ch) {
			i++
			continue
		}
		if isQuote(ch) {
			j := i + 1
			var buf []rune
			for j < n && !isQuote(runes[j]) {
				buf = append(buf, runes[j])
				j++
			}
			if text := strings.TrimSpace(string(buf)); text != "" {
				tokens = append(tokens, rawToken{text: text, quoted: true})
			}
			i = j + 1
			continue
		}
		// Bare word; a quote directly after `name:` swallows the quoted span
		// into the same token (intitle:"budget tips").
		var buf []rune
		quotedValue := false
		for i < n && !unicode.IsSpace(runes[i]) {
			c := runes[i]
			if isQuote(c) && len(buf) > 0 && buf[len(buf)-1] == ':' {
				j := i + 1
				for j < n && !isQuote(runes[j]) {
					buf = append(buf, runes[j])
					j++
				}
				quotedValue = true
				i = j + 1
				continue
			}
			if isQuote(c) {
				break // foo"bar — stop the word, let the quote start a phrase
			}
			buf = append(buf, c)
			i++
		}
		if len(buf) > 0 {
			tokens = append(tokens, rawToken{text: string(buf), quotedValue: quotedValue})
		}
	}
	return splitParens(tokens)
}

// splitParens splits leading ( and unbalanced trailing ) into standalone
// tokens so (react OR vue) parses while site:wikipedia.org/Foo_(bar) stays
// whole.
func splitParens(tokens []rawToken) []rawToken {
	var out []rawToken
	for _, tok := range tokens {
		if tok.quoted || tok.quotedValue {
			out = append(out, tok)
			continue
		}
		text := tok.text
		for strings.HasPrefix(text, "(") {
			out = append(out, rawToken{text: "("})
			text = text[1:]
		}
		trailing := 0
		for strings.HasSuffix(text, ")") {
			// Only strip parens that do not close an opener inside the word.
			body := text[:len(text)-1]
			depth := 0
			for _, c := range body {
				switch c {
				case '(':
					depth++
				case ')':
					depth--
				}
			}
			if depth > 0 {
				break
			}
			text = body
			trailing++
		}
		if text != "" {
			out = append(out, rawToken{text: text})
		}
		for k := 0; k < trailing; k++ {
			out = append(out, rawToken{text: ")"})
		}
	}
	return out
}

// isoDate converts year/month/day parts to a validated ISO date; "" = invalid.
func isoDate(year, month, day int) string {
	if year < 1000 || year > 9999 || month < 1 || month > 12 || day < 1 || day > 31 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

var dateYMDPattern = regexp.MustCompile(`^(\d{4})(?:[-/.](\d{1,2})(?:[-/.](\d{1,2}))?)?$`)
var dateMDYPattern = regexp.MustCompile(`^(\d{1,2})[-/.](\d{1,2})[-/.](\d{4})$`)

// ParseDateValue parses a before:/after: value into ISO YYYY-MM-DD; "" =
// invalid. Accepts YYYY, YYYY-MM, YYYY-MM-DD (also / and . separators) and
// MM/DD/YYYY (day-first assumed when the first field exceeds 12). Bare
// years/months resolve to the first day of the period, matching Google's
// after:2024 ≙ after:2024-01-01 semantics.
func ParseDateValue(value string) string {
	t := strings.TrimSpace(value)
	if m := dateYMDPattern.FindStringSubmatch(t); m != nil {
		month, day := 1, 1
		if m[2] != "" {
			month = atoi(m[2])
		}
		if m[3] != "" {
			day = atoi(m[3])
		}
		return isoDate(atoi(m[1]), month, day)
	}
	if m := dateMDYPattern.FindStringSubmatch(t); m != nil {
		month := atoi(m[1])
		day := atoi(m[2])
		if month > 12 && day <= 12 {
			month, day = day, month
		}
		return isoDate(atoi(m[3]), month, day)
	}
	return ""
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

var siteSchemePattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://`)
var siteTrailingPattern = regexp.MustCompile(`[/.]+$`)

// normalizeSite lowercases a site: value and strips scheme, *. wildcard, and
// trailing slash/dot.
func normalizeSite(value string) string {
	site := strings.ToLower(strings.TrimSpace(value))
	site = siteSchemePattern.ReplaceAllString(site, "")
	site = strings.TrimPrefix(site, "*.")
	site = siteTrailingPattern.ReplaceAllString(site, "")
	return site
}

// directiveFields maps directive names to their canonical field.
var directiveFields = map[string]string{
	"site":     "site",
	"domain":   "site",
	"host":     "site",
	"inurl":    "inUrl",
	"url":      "inUrl",
	"intitle":  "inTitle",
	"title":    "inTitle",
	"intext":   "inText",
	"inbody":   "inText",
	"inanchor": "inText",
	"filetype": "filetype",
	"ext":      "filetype",
	"before":   "before",
	"until":    "before",
	"after":    "after",
	"since":    "after",
	"lang":     "lang",
	"language": "lang",
}

// allModes maps allin*: directives that capture every following plain term.
var allModes = map[string]allModeField{
	"allintitle": allModeInTitle,
	"allinurl":   allModeInURL,
	"allintext":  allModeInText,
}

// isReservedToken is true for operator/paren tokens and recognized directives
// — anything a bare name: must not adopt as its value.
func isReservedToken(text string) bool {
	switch text {
	case "(", ")", "OR", "AND", "NOT", "|", "||", "&&", "!":
		return true
	}
	m := directivePattern.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	name := strings.ToLower(m[2])
	_, isField := directiveFields[name]
	_, isAll := allModes[name]
	return isField || isAll
}

// ParseSearchQuery parses a raw query into a StructuredQuery.
//
// Lenient by construction: unknown name:value tokens (URLs, C:\paths, TS2345:,
// jargon) stay in the free text verbatim, and a directive with an unparseable
// value (before:someday) degrades to a plain term instead of being dropped.
func ParseSearchQuery(raw string) StructuredQuery {
	q := StructuredQuery{Raw: raw}

	tokens := tokenize(raw)
	negateNext := false
	orPending := false
	lastWasTerm := false
	groupSeq := 0
	var allMode allModeField

	pushConstraint := func(field, value string, negated bool) {
		q.HasDirectives = true
		orPending = false
		lastWasTerm = false
		v := strings.TrimSpace(value)
		if v == "" {
			return
		}
		switch field {
		case "site":
			if site := normalizeSite(v); site != "" {
				if negated {
					q.ExcludedSites = append(q.ExcludedSites, site)
				} else {
					q.Sites = append(q.Sites, site)
				}
			}
		case "inUrl":
			if negated {
				q.ExcludedInURL = append(q.ExcludedInURL, v)
			} else {
				q.InURL = append(q.InURL, v)
			}
		case "inTitle":
			if negated {
				q.ExcludedInTitle = append(q.ExcludedInTitle, v)
			} else {
				q.InTitle = append(q.InTitle, v)
			}
		case "inText":
			if negated {
				q.ExcludedInText = append(q.ExcludedInText, v)
			} else {
				q.InText = append(q.InText, v)
			}
		case "filetype":
			if ext := strings.TrimLeft(strings.ToLower(v), "."); ext != "" {
				if negated {
					q.ExcludedFiletypes = append(q.ExcludedFiletypes, ext)
				} else {
					q.Filetypes = append(q.Filetypes, ext)
				}
			}
		}
	}

	pushTerm := func(text string, phrase bool) {
		negated := negateNext
		negateNext = false
		if allMode != "" {
			pushConstraint(string(allMode), text, negated)
			return
		}
		term := QueryTerm{Text: text}
		if phrase {
			term.Phrase = true
		}
		if negated {
			term.Negated = true
		}
		if orPending && lastWasTerm {
			if len(q.Terms) > 0 {
				prev := &q.Terms[len(q.Terms)-1]
				if prev.Group == 0 {
					groupSeq++
					prev.Group = groupSeq
				}
				term.Group = prev.Group
			}
		}
		orPending = false
		lastWasTerm = true
		q.Terms = append(q.Terms, term)
	}

	for idx := 0; idx < len(tokens); idx++ {
		tok := tokens[idx]

		if tok.quoted {
			pushTerm(tok.text, true)
			continue
		}

		// Boolean operators and grouping parens.
		switch tok.text {
		case "(", ")":
			continue
		case "OR", "|", "||":
			orPending = true
			q.HasDirectives = true
			continue
		case "AND", "&&":
			q.HasDirectives = true
			continue
		case "NOT", "!":
			negateNext = true
			q.HasDirectives = true
			continue
		case "-", "+":
			// The tokenizer splits -"exact phrase" into - + phrase; carry the
			// negation over.
			if tok.text == "-" && idx+1 < len(tokens) && tokens[idx+1].quoted {
				negateNext = true
			}
			continue
		}

		match := directivePattern.FindStringSubmatch(tok.text)
		var name string
		if match != nil {
			name = strings.ToLower(match[2])
		}
		allModeHit, hasAllMode := allModes[name]
		field, hasField := directiveFields[name]

		if match != nil && hasAllMode {
			allMode = allModeHit
			q.HasDirectives = true
			// allintitle:budget tips — inline value plus every following term.
			if inline := strings.TrimSpace(match[3]); inline != "" {
				pushConstraint(string(allModeHit), inline, match[1] == "-")
			}
			orPending = false
			lastWasTerm = false
			continue
		}

		if match != nil && hasField {
			value := strings.TrimSpace(match[3])
			// site: example.com — lenient: adopt the next plain token as the value.
			if value == "" && idx+1 < len(tokens) {
				next := tokens[idx+1]
				if next.quoted || !isReservedToken(next.text) {
					value = strings.TrimSpace(next.text)
					idx++
				}
			}
			if value == "" {
				q.HasDirectives = true
				continue
			}
			negated := match[1] == "-" || negateNext
			negateNext = false
			switch field {
			case "before", "after":
				iso := ParseDateValue(value)
				if iso == "" {
					pushTerm(tok.text, false)
					continue
				}
				if field == "before" {
					q.Before = iso
				} else {
					q.After = iso
				}
				q.HasDirectives = true
				orPending = false
				lastWasTerm = false
			case "lang":
				q.Lang = strings.ToLower(value)
				q.HasDirectives = true
				orPending = false
				lastWasTerm = false
			default:
				pushConstraint(field, value, negated)
			}
			continue
		}

		// Plain term with optional +/- prefix.
		text := tok.text
		if strings.HasPrefix(text, "-") && len(text) > 1 {
			negateNext = true
			q.HasDirectives = true
			text = strings.TrimLeft(text, "-")
			if text == "" {
				continue
			}
			// -site:x arrives pre-split only when written "- site:x"; re-check
			// directive. before/after/lang are excluded here: the double dash
			// defeated the main pattern, so --before: reaches this path and
			// must become a NEGATED plain term, not a constraint.
			negMatch := directivePattern.FindStringSubmatch(text)
			var negField string
			if negMatch != nil {
				negField = directiveFields[strings.ToLower(negMatch[2])]
			}
			if negMatch != nil && negField != "" && negField != "before" && negField != "after" && negField != "lang" {
				negateNext = false
				pushConstraint(negField, strings.TrimSpace(negMatch[3]), true)
				continue
			}
			pushTerm(text, false)
			continue
		}
		if strings.HasPrefix(text, "+") && len(text) > 1 {
			// Legacy Google +term: verbatim/required — treat as an exact phrase.
			pushTerm(text[1:], true)
			q.HasDirectives = true
			continue
		}
		pushTerm(text, false)
	}

	q.Text = renderTerms(q.Terms, QuerySyntax{Phrases: true, Negation: true, Or: true})
	q.HasConstraints = len(q.Sites) != 0 ||
		len(q.ExcludedSites) != 0 ||
		len(q.InURL) != 0 ||
		len(q.ExcludedInURL) != 0 ||
		len(q.InTitle) != 0 ||
		len(q.ExcludedInTitle) != 0 ||
		len(q.Filetypes) != 0 ||
		len(q.ExcludedFiletypes) != 0 ||
		q.Before != "" ||
		q.After != ""
	return q
}

var relativeAgePattern = regexp.MustCompile(`(?i)^(\d+)\s*(minute|min|hour|hr|day|week|month|mo|year|yr|[mhdwy])s?\s+ago$`)

var relativeUnitSeconds = map[string]int64{
	"m":      60,
	"min":    60,
	"minute": 60,
	"h":      3600,
	"hr":     3600,
	"hour":   3600,
	"d":      86400,
	"day":    86400,
	"w":      604800,
	"week":   604800,
	"mo":     2592000,
	"month":  2592000,
	"y":      31536000,
	"yr":     31536000,
	"year":   31536000,
}

// sourceTime returns the best-effort publish time of a source; ok=false when
// undated. AgeSeconds wins when non-zero (gbot's 0 = unknown sentinel; TS
// treats finite 0 as "now"). Fallbacks: relative ("3 days ago") then the
// shared dateAgeLayouts.
func sourceTime(source SearchSource) (t time.Time, ok bool) {
	if source.AgeSeconds != 0 {
		return time.Now().Add(-time.Duration(source.AgeSeconds) * time.Second), true
	}
	if source.PublishedDate == "" {
		return time.Time{}, false
	}
	if m := relativeAgePattern.FindStringSubmatch(strings.TrimSpace(source.PublishedDate)); m != nil {
		unit := relativeUnitSeconds[strings.ToLower(m[2])]
		if seconds := atoi64(m[1]) * unit; seconds > 0 {
			return time.Now().Add(-time.Duration(seconds) * time.Second), true
		}
		return time.Time{}, false
	}
	for _, layout := range dateAgeLayouts {
		if t, err := time.Parse(layout, source.PublishedDate); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func atoi64(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}

// hostAndPath returns the hostname (lowercased) and pathname of a URL.
// ok=false when unparsable or hostless: JS new URL throws on scheme-less
// inputs (example.com/x) while Go url.Parse accepts them with an empty host,
// so the empty-host rule keeps Go from suffix-matching paths JS never sees.
func hostAndPath(rawURL string) (host, path string, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", false
	}
	host = strings.ToLower(u.Hostname())
	if host == "" {
		return "", "", false
	}
	return host, u.Path, true
}

// MatchesSite is the site: matcher: exact host or subdomain of site; when
// site carries a path (github.com/anthropics), the URL path must start with
// it (case-insensitively).
func MatchesSite(rawURL, site string) bool {
	host, path, ok := hostAndPath(rawURL)
	if !ok {
		return false
	}
	var siteHost, sitePath string
	if slash := strings.Index(site, "/"); slash == -1 {
		siteHost = site
	} else {
		siteHost = site[:slash]
		sitePath = site[slash:]
	}
	if host != siteHost && !strings.HasSuffix(host, "."+siteHost) {
		return false
	}
	if sitePath != "" && !strings.HasPrefix(strings.ToLower(path), strings.ToLower(sitePath)) {
		return false
	}
	return true
}

// matchesFiletype is the filetype: matcher: URL pathname ends with .ext.
func matchesFiletype(rawURL, ext string) bool {
	_, path, ok := hostAndPath(rawURL)
	if !ok {
		return false
	}
	return strings.HasSuffix(strings.ToLower(path), "."+ext)
}

// quoteValue quotes a directive value when it contains whitespace.
func quoteValue(value string) string {
	for _, r := range value {
		if unicode.IsSpace(r) {
			return `"` + value + `"`
		}
	}
	return value
}

// renderTerm renders one free-text term per the target syntax; ok=false when
// the term is dropped (negated under a syntax without negation).
func renderTerm(term QueryTerm, syntax QuerySyntax) (string, bool) {
	if term.Negated && !syntax.Negation {
		return "", false
	}
	body := term.Text
	if term.Phrase && syntax.Phrases {
		body = `"` + term.Text + `"`
	}
	if term.Negated {
		return "-" + body, true
	}
	return body, true
}

// renderTerms renders the free-text terms per the target syntax.
func renderTerms(terms []QueryTerm, syntax QuerySyntax) string {
	var parts []string
	for i := 0; i < len(terms); i++ {
		term := terms[i]
		if term.Group != 0 && syntax.Or {
			var members []string
			j := i
			for ; j < len(terms) && terms[j].Group == term.Group; j++ {
				if rendered, ok := renderTerm(terms[j], syntax); ok {
					members = append(members, rendered)
				}
			}
			i = j - 1
			switch len(members) {
			case 0:
			case 1:
				parts = append(parts, members[0])
			default:
				parts = append(parts, "("+strings.Join(members, " OR ")+")")
			}
			continue
		}
		if rendered, ok := renderTerm(term, syntax); ok {
			parts = append(parts, rendered)
		}
	}
	return strings.Join(parts, " ")
}

// FormatQuery rebuilds a query string for an engine with the given
// QuerySyntax.
//
// Constraints whose syntax the engine lacks are omitted (the caller maps
// them onto API parameters or relies on ApplyQueryConstraints). Never
// returns an empty string for a non-empty input: a directives-only query
// falls back to the constraint values as keywords, then to Raw — an engine
// searching *something* beats an empty-query error.
func FormatQuery(q StructuredQuery, syntax QuerySyntax) string {
	var parts []string
	if text := renderTerms(q.Terms, syntax); text != "" {
		parts = append(parts, text)
	}

	if syntax.Site {
		if len(q.Sites) > 1 && syntax.Or {
			siteParts := make([]string, len(q.Sites))
			for i, s := range q.Sites {
				siteParts[i] = "site:" + s
			}
			parts = append(parts, "("+strings.Join(siteParts, " OR ")+")")
		} else {
			for _, s := range q.Sites {
				parts = append(parts, "site:"+s)
			}
		}
		for _, s := range q.ExcludedSites {
			parts = append(parts, "-site:"+s)
		}
	}
	if syntax.InURL {
		for _, v := range q.InURL {
			parts = append(parts, "inurl:"+quoteValue(v))
		}
		for _, v := range q.ExcludedInURL {
			parts = append(parts, "-inurl:"+quoteValue(v))
		}
	}
	if syntax.InTitle {
		for _, v := range q.InTitle {
			parts = append(parts, "intitle:"+quoteValue(v))
		}
		for _, v := range q.ExcludedInTitle {
			parts = append(parts, "-intitle:"+quoteValue(v))
		}
	}
	if syntax.InText {
		for _, v := range q.InText {
			parts = append(parts, "intext:"+quoteValue(v))
		}
		for _, v := range q.ExcludedInText {
			parts = append(parts, "-intext:"+quoteValue(v))
		}
	}
	if syntax.Filetype {
		if len(q.Filetypes) > 1 && syntax.Or {
			ftParts := make([]string, len(q.Filetypes))
			for i, f := range q.Filetypes {
				ftParts[i] = "filetype:" + f
			}
			parts = append(parts, "("+strings.Join(ftParts, " OR ")+")")
		} else {
			for _, f := range q.Filetypes {
				parts = append(parts, "filetype:"+f)
			}
		}
		for _, f := range q.ExcludedFiletypes {
			parts = append(parts, "-filetype:"+f)
		}
	}
	if syntax.DateRange {
		if q.After != "" {
			parts = append(parts, "after:"+q.After)
		}
		if q.Before != "" {
			parts = append(parts, "before:"+q.Before)
		}
	}

	result := strings.TrimSpace(strings.Join(parts, " "))
	if result == "" {
		// Directives-only query and no directive syntax: search the constraint
		// values as plain keywords so the engine still gets a meaningful query.
		fallback := append([]string{}, q.Sites...)
		fallback = append(fallback, q.InTitle...)
		fallback = append(fallback, q.InURL...)
		fallback = append(fallback, q.InText...)
		fallback = append(fallback, q.Filetypes...)
		result = strings.TrimSpace(strings.Join(fallback, " "))
	}
	if result == "" {
		return strings.TrimSpace(q.Raw)
	}
	return result
}

// FormatScraperQuery builds the engine query for a credential-free HTML
// engine (Google, Startpage, DuckDuckGo, Ecosia, Mojeek, SearXNG).
//
// Canonicalizes directives via FormatQuery with the engine's QuerySyntax
// (default: full Google syntax), after demoting the operators that
// zero-match across the scraper set: engines only match site: against a bare
// domain (a path yields zero results everywhere), and DuckDuckGo ignores
// inurl: entirely — so either operator silently empties the result set. The
// raw URL as a plain term matches fine, so bare-domain site: filters are kept
// while path-carrying site: and all inurl: values become plain keywords; the
// demotion is structural (before formatting), so OR-grouped and quoted
// directives are covered. Negated forms (-site:, -inurl:) pass through
// untouched — demoting them would invert an exclusion into a search term;
// the pipeline post-filter (ApplyQueryConstraints) enforces every demoted or
// unsupported constraint on the returned sources. Directive-free queries
// pass through byte-identical.
func FormatScraperQuery(query string, parsedQuery *StructuredQuery, syntax QuerySyntax) string {
	var parsed StructuredQuery
	if parsedQuery != nil {
		parsed = *parsedQuery
	} else {
		parsed = ParseSearchQuery(query)
	}
	if !parsed.HasDirectives {
		return query
	}
	var demoted []string
	var bareSites []string
	for _, site := range parsed.Sites {
		if strings.Contains(site, "/") {
			demoted = append(demoted, site)
		} else {
			bareSites = append(bareSites, site)
		}
	}
	demoted = append(demoted, parsed.InURL...)
	downgraded := parsed
	downgraded.Sites = bareSites
	downgraded.InURL = nil
	for _, text := range demoted {
		downgraded.Terms = append(downgraded.Terms, QueryTerm{Text: text})
	}
	return FormatQuery(downgraded, syntax)
}

type constraintDimension struct {
	// label is the directive rendering for relaxation notes (site:arxiv.org).
	label string
	pred  func(source SearchSource) bool
}

func constraintDimensions(q StructuredQuery) []constraintDimension {
	var dims []constraintDimension

	if len(q.Sites) != 0 {
		labels := make([]string, len(q.Sites))
		for i, s := range q.Sites {
			labels[i] = "site:" + s
		}
		sites := q.Sites
		dims = append(dims, constraintDimension{
			label: strings.Join(labels, " OR "),
			pred: func(src SearchSource) bool {
				for _, site := range sites {
					if MatchesSite(src.URL, site) {
						return true
					}
				}
				return false
			},
		})
	}
	if len(q.ExcludedSites) != 0 {
		labels := make([]string, len(q.ExcludedSites))
		for i, s := range q.ExcludedSites {
			labels[i] = "-site:" + s
		}
		excluded := q.ExcludedSites
		dims = append(dims, constraintDimension{
			label: strings.Join(labels, " "),
			pred: func(src SearchSource) bool {
				for _, site := range excluded {
					if MatchesSite(src.URL, site) {
						return false
					}
				}
				return true
			},
		})
	}
	if len(q.InURL) != 0 {
		labels := make([]string, len(q.InURL))
		for i, v := range q.InURL {
			labels[i] = "inurl:" + v
		}
		values := q.InURL
		dims = append(dims, constraintDimension{
			label: strings.Join(labels, " "),
			pred: func(src SearchSource) bool {
				url := strings.ToLower(src.URL)
				for _, v := range values {
					if !strings.Contains(url, strings.ToLower(v)) {
						return false
					}
				}
				return true
			},
		})
	}
	if len(q.ExcludedInURL) != 0 {
		labels := make([]string, len(q.ExcludedInURL))
		for i, v := range q.ExcludedInURL {
			labels[i] = "-inurl:" + v
		}
		excluded := q.ExcludedInURL
		dims = append(dims, constraintDimension{
			label: strings.Join(labels, " "),
			pred: func(src SearchSource) bool {
				url := strings.ToLower(src.URL)
				for _, v := range excluded {
					if strings.Contains(url, strings.ToLower(v)) {
						return false
					}
				}
				return true
			},
		})
	}
	if len(q.InTitle) != 0 {
		labels := make([]string, len(q.InTitle))
		for i, v := range q.InTitle {
			labels[i] = "intitle:" + v
		}
		values := q.InTitle
		dims = append(dims, constraintDimension{
			label: strings.Join(labels, " "),
			pred: func(src SearchSource) bool {
				title := strings.ToLower(src.Title)
				for _, v := range values {
					if !strings.Contains(title, strings.ToLower(v)) {
						return false
					}
				}
				return true
			},
		})
	}
	if len(q.ExcludedInTitle) != 0 {
		labels := make([]string, len(q.ExcludedInTitle))
		for i, v := range q.ExcludedInTitle {
			labels[i] = "-intitle:" + v
		}
		excluded := q.ExcludedInTitle
		dims = append(dims, constraintDimension{
			label: strings.Join(labels, " "),
			pred: func(src SearchSource) bool {
				title := strings.ToLower(src.Title)
				for _, v := range excluded {
					if strings.Contains(title, strings.ToLower(v)) {
						return false
					}
				}
				return true
			},
		})
	}
	if len(q.Filetypes) != 0 {
		labels := make([]string, len(q.Filetypes))
		for i, f := range q.Filetypes {
			labels[i] = "filetype:" + f
		}
		filetypes := q.Filetypes
		dims = append(dims, constraintDimension{
			label: strings.Join(labels, " OR "),
			pred: func(src SearchSource) bool {
				for _, ext := range filetypes {
					if matchesFiletype(src.URL, ext) {
						return true
					}
				}
				return false
			},
		})
	}
	if len(q.ExcludedFiletypes) != 0 {
		labels := make([]string, len(q.ExcludedFiletypes))
		for i, f := range q.ExcludedFiletypes {
			labels[i] = "-filetype:" + f
		}
		excluded := q.ExcludedFiletypes
		dims = append(dims, constraintDimension{
			label: strings.Join(labels, " "),
			pred: func(src SearchSource) bool {
				for _, ext := range excluded {
					if matchesFiletype(src.URL, ext) {
						return false
					}
				}
				return true
			},
		})
	}
	if q.After != "" || q.Before != "" {
		// Parse failures treat the bound as unset: TS NaN comparisons never
		// violate either.
		var afterTime, beforeTime time.Time
		hasAfter, hasBefore := false, false
		if q.After != "" {
			if t, err := time.Parse("2006-01-02", q.After); err == nil {
				afterTime, hasAfter = t, true
			}
		}
		if q.Before != "" {
			if t, err := time.Parse("2006-01-02", q.Before); err == nil {
				beforeTime, hasBefore = t, true
			}
		}
		var labelParts []string
		if q.After != "" {
			labelParts = append(labelParts, "after:"+q.After)
		}
		if q.Before != "" {
			labelParts = append(labelParts, "before:"+q.Before)
		}
		dims = append(dims, constraintDimension{
			label: strings.Join(labelParts, " "),
			pred: func(src SearchSource) bool {
				t, dated := sourceTime(src)
				if !dated {
					return true // undated → cannot prove violation
				}
				if hasAfter && t.Before(afterTime) {
					return false
				}
				if hasBefore && !t.Before(beforeTime) {
					return false
				}
				return true
			},
		})
	}
	return dims
}

// MatchesQueryConstraints is the strict per-source constraint check: every
// filterable dimension of q must pass. Sources without a resolvable date
// pass date bounds (a missing date is not proof of violation). For custom
// provider flows; the standard path is ApplyQueryConstraints.
func MatchesQueryConstraints(source SearchSource, q StructuredQuery) bool {
	for _, dim := range constraintDimensions(q) {
		if !dim.pred(source) {
			return false
		}
	}
	return true
}

// ConstraintFilterResult is the result of ApplyQueryConstraints.
type ConstraintFilterResult struct {
	// Sources surviving the lenient filter — never empty when the input was
	// non-empty.
	Sources []SearchSource
	// Dropped holds the directive renderings (site:arxiv.org,
	// before:2024-01-01, …) of the constraint dimensions that matched zero
	// sources and were therefore relaxed instead of enforced.
	Dropped []string
}

// ApplyQueryConstraints applies each constraint dimension of q in turn,
// skipping (and reporting) any dimension that would eliminate every remaining
// source. Guarantees a non-empty result for a non-empty input, so a
// mis-scoped directive degrades to unfiltered results plus a note instead of
// a dead search.
func ApplyQueryConstraints(sources []SearchSource, q StructuredQuery) ConstraintFilterResult {
	current := make([]SearchSource, len(sources))
	copy(current, sources)
	var dropped []string
	if len(current) == 0 {
		return ConstraintFilterResult{Sources: current, Dropped: dropped}
	}
	for _, dim := range constraintDimensions(q) {
		var kept []SearchSource
		for _, src := range current {
			if dim.pred(src) {
				kept = append(kept, src)
			}
		}
		if len(kept) != 0 {
			current = kept
		} else {
			dropped = append(dropped, dim.label)
		}
	}
	return ConstraintFilterResult{Sources: current, Dropped: dropped}
}
