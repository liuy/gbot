package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/PuerkitoBio/goquery"
)

const (
	ddgUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	ddgTimeout   = 20 * time.Second
	// The render phase gets its own window from the parent ctx: a cold
	// Chrome start (5-10s) inside the shared 20s fetch budget would
	// starve the render.
	ddgBrowseTimeout = 25 * time.Second
)

// DDGSearchURL is the cross-package test seam for the real-Chrome
// integration test in the web package.
var DDGSearchURL = "https://html.duckduckgo.com/html/"

// ddgQuerySyntax is the query syntax the DDG HTML frontend parses: quotes,
// -, OR, site:, filetype:, intitle:, inurl:, intext:. Date bounds
// (before:/after:) are deliberately off — DDG does not parse them, so they
// are stripped from the query and enforced by the pipeline's lenient
// post-filter instead (omp duckduckgo.ts DDG_QUERY_SYNTAX).
var ddgQuerySyntax = QuerySyntax{
	Phrases:  true,
	Negation: true,
	Or:       true,
	Site:     true,
	InURL:    true,
	InTitle:  true,
	InText:   true,
	Filetype: true,
}

// recencyToDDGDf maps the recency window onto DDG's df form param; DDG
// accepts single letters for the time filter, and queries without df return
// the unfiltered default.
var recencyToDDGDf = map[string]string{
	"day":   "d",
	"week":  "w",
	"month": "m",
	"year":  "y",
}

// ddgKLCodes is DuckDuckGo's documented kl set. The codes resemble
// region-language locales but contain provider-specific identifiers (jp-jp,
// tw-tzh, uk-en) that cannot be derived mechanically.
var ddgKLCodes = map[string]bool{
	"xa-ar": true, "xa-en": true, "ar-es": true, "au-en": true,
	"at-de": true, "be-fr": true, "be-nl": true, "br-pt": true,
	"bg-bg": true, "ca-en": true, "ca-fr": true, "ct-ca": true,
	"cl-es": true, "cn-zh": true, "co-es": true, "hr-hr": true,
	"cz-cs": true, "dk-da": true, "ee-et": true, "fi-fi": true,
	"fr-fr": true, "de-de": true, "gr-el": true, "hk-tzh": true,
	"hu-hu": true, "in-en": true, "id-id": true, "id-en": true,
	"ie-en": true, "il-he": true, "it-it": true, "jp-jp": true,
	"kr-kr": true, "lv-lv": true, "lt-lt": true, "xl-es": true,
	"my-ms": true, "my-en": true, "mx-es": true, "nl-nl": true,
	"nz-en": true, "no-no": true, "pe-es": true, "ph-en": true,
	"ph-tl": true, "pl-pl": true, "pt-pt": true, "ro-ro": true,
	"ru-ru": true, "sg-en": true, "sk-sk": true, "sl-sl": true,
	"za-en": true, "es-es": true, "se-sv": true, "ch-de": true,
	"ch-fr": true, "ch-it": true, "tw-tzh": true, "th-th": true,
	"tr-tr": true, "ua-uk": true, "uk-en": true, "us-en": true,
	"ue-es": true, "ve-es": true, "vn-vi": true, "wt-wt": true,
}

// ddgLocaleAliases holds BCP 47 locales whose DDG code does not follow a
// simple component swap.
var ddgLocaleAliases = map[string]string{
	"ca-es":  "ct-ca",
	"en-gb":  "uk-en",
	"es-419": "xl-es",
	"es-us":  "ue-es",
	"ja-jp":  "jp-jp",
	"ko-kr":  "kr-kr",
	"zh-hk":  "hk-tzh",
	"zh-tw":  "tw-tzh",
}

var ddgLocalePattern = regexp.MustCompile(`^([a-z]{2})-([a-z]{2})$`)

// localeToKl maps a parsed lang: locale onto DuckDuckGo's documented kl
// values. Shared queries use language-region order while DDG generally uses
// region-language; provider-specific exceptions resolve through
// ddgLocaleAliases, and all other values must survive the documented
// allowlist after swapping or the caller keeps its default region.
func localeToKl(lang string) string {
	if lang == "" {
		return ""
	}
	locale := strings.ToLower(strings.ReplaceAll(lang, "_", "-"))
	if alias, ok := ddgLocaleAliases[locale]; ok {
		return alias
	}
	m := ddgLocalePattern.FindStringSubmatch(locale)
	if m == nil {
		return ""
	}
	candidate := m[2] + "-" + m[1]
	if ddgKLCodes[candidate] {
		return candidate
	}
	return ""
}

// ddgPublishedLayouts parses the no-offset datetimes DDG emits
// (2026-07-30T20:19:00.0000000). The shared dateAgeLayouts all fail that form
// (RFC3339 wants an offset), which would zero AgeSeconds and make the central
// date filter dead code on ddg. Go time.Parse accepts a fractional-second
// tail immediately after seconds even when the layout omits it, so the
// .0000000 suffix needs no entry. Parsed as UTC where omp's JS reads local
// time (A11 divergence, accepted: day-granularity bounds are unaffected).
var ddgPublishedLayouts = []string{
	"2006-01-02T15:04:05", "2006-01-02 15:04:05", // extraction regex permits both separators
	"2006-01-02T15:04", "2006-01-02 15:04", // alternative without seconds
	"2006-01-02", // bare-date alternative
}

// ddgDateTextPattern matches the timestamp shapes extractPublishedDate
// attributes: a date, optionally with HH:MM (any tail after the minutes is
// accepted — DDG emits .0000000 fractions).
var ddgDateTextPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}|$)`)

// normalizeDDGText collapses whitespace runs (NBSP included — goquery decodes
// &nbsp; to U+00A0) to single spaces and trims, mirroring omp decodeHtmlText.
func normalizeDDGText(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// extractDDGPublishedDate lifts a result row's publication timestamp. Recent
// DDG HTML renders it as a bare span inside result__extras__url (often with
// leading &nbsp;). The scan is restricted to that container so a date-shaped
// value in a result__snippet is not attributed as publication metadata.
func extractDDGPublishedDate(block *goquery.Selection) string {
	found := ""
	block.Find(".result__extras__url span").EachWithBreak(func(_ int, span *goquery.Selection) bool {
		text := normalizeDDGText(span.Text())
		if ddgDateTextPattern.MatchString(text) {
			found = text
			return false
		}
		return true
	})
	return found
}

type DuckDuckGoProvider struct {
	Client *http.Client

	// Browse renders a URL with headless Chrome. Nil = no escalation channel.
	Browse func(ctx context.Context, url string, timeout time.Duration) (string, error)
}

func (d *DuckDuckGoProvider) ID() string        { return "duckduckgo" }
func (d *DuckDuckGoProvider) IsAvailable() bool { return true }

func (d *DuckDuckGoProvider) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return http.DefaultClient
}

func (d *DuckDuckGoProvider) Search(ctx context.Context, params SearchParams) (*SearchResponse, error) {
	parsed := params.Parsed()
	vals := url.Values{}
	vals.Set("q", FormatScraperQuery(params.Query, parsed, ddgQuerySyntax))
	// kl only when lang maps (no default region pin — directive-free queries
	// keep their existing behavior); df only when recency is set.
	if kl := localeToKl(parsed.Lang); kl != "" {
		vals.Set("kl", kl)
	}
	if df, ok := recencyToDDGDf[params.Recency]; ok {
		vals.Set("df", df)
	}
	searchURL := DDGSearchURL + "?" + vals.Encode()
	return d.searchWithClient(ctx, params, searchURL)
}

// searchViaBrowser renders searchURL headlessly and parses it with the
// same parser as the plain path. Fresh deadline from the parent ctx:
// the plain-fetch budget must not starve a cold Chrome start.
func (d *DuckDuckGoProvider) searchViaBrowser(ctx context.Context, searchURL string, params SearchParams) (*SearchResponse, error) {
	browseCtx, cancel := context.WithTimeout(ctx, ddgBrowseTimeout)
	defer cancel()

	html, err := d.Browse(browseCtx, searchURL, ddgBrowseTimeout)
	if err != nil {
		return nil, &SearchProviderError{
			Provider: "duckduckgo",
			Message:  fmt.Sprintf("DuckDuckGo browser fallback failed: %v", err),
			Status:   503,
		}
	}
	if isAnomalyResponse(html) {
		return nil, ddgBlockedError()
	}
	sources, err := parseDDGHTML(strings.NewReader(html), params.Limit)
	if err != nil {
		return nil, fmt.Errorf("ddg: parse: %w", err)
	}
	return &SearchResponse{Provider: "duckduckgo", Sources: sources}, nil
}

func (d *DuckDuckGoProvider) searchWithClient(ctx context.Context, params SearchParams, searchURL string) (*SearchResponse, error) {

	reqCtx, cancel := context.WithTimeout(ctx, ddgTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ddg: build request: %w", err)
	}
	req.Header.Set("User-Agent", ddgUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := d.client().Do(req)
	if err != nil {
		// Escalation is reserved for bot-block signals; a parent ctx that is
		// already canceled must surface, not spawn a doomed render.
		if d.Browse != nil && ctx.Err() == nil {
			return d.searchViaBrowser(ctx, searchURL, params)
		}
		return nil, &SearchProviderError{
			Provider: "duckduckgo",
			Message:  fmt.Sprintf("request failed: %v", err),
			Status:   0,
		}
	}
	defer resp.Body.Close()

	// Read the body up front: the anomaly check needs it even on non-2xx,
	// so one read rule covers both branches.
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SearchProviderError{
			Provider: "duckduckgo",
			Message:  fmt.Sprintf("read response: %v", err),
			Status:   resp.StatusCode,
		}
	}
	body := string(bodyBytes)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		if d.Browse != nil {
			return d.searchViaBrowser(ctx, searchURL, params)
		}
		return nil, &SearchProviderError{
			Provider: "duckduckgo",
			Message:  fmt.Sprintf("HTTP %d", resp.StatusCode),
			Status:   resp.StatusCode,
		}
	}

	if isAnomalyResponse(body) {
		if d.Browse != nil {
			return d.searchViaBrowser(ctx, searchURL, params)
		}
		return nil, ddgBlockedError()
	}

	sources, err := parseDDGHTML(strings.NewReader(body), params.Limit)
	if err != nil {
		return nil, fmt.Errorf("ddg: parse: %w", err)
	}

	return &SearchResponse{
		Provider: "duckduckgo",
		Sources:  sources,
	}, nil
}

// isAnomalyResponse reports whether the page DDG returned is the
// bot-challenge modal instead of real results. DDG mixes status codes
// (200 vs 202) on challenge pages, so the body check is the only
// reliable signal; empty results is a legitimate no-match outcome.
func isAnomalyResponse(html string) bool {
	return strings.Contains(html, "anomaly-modal") || strings.Contains(html, "anomaly.js")
}

// ddgBlockedError is shared by the plain and browser-escalation paths so the
// message never drifts between them.
func ddgBlockedError() *SearchProviderError {
	return &SearchProviderError{
		Provider: "duckduckgo",
		Message: "DuckDuckGo blocked the request with a bot-detection challenge. " +
			"DuckDuckGo throttles automated HTML searches from datacenter/shared-egress IPs; " +
			"configure a credentialed provider such as Jina or Tavily for reliable web search.",
		Status: 429,
	}
}

func parseDDGHTML(body io.Reader, limit int) ([]SearchSource, error) {
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}

	var sources []SearchSource
	doc.Find(".result").Each(func(i int, s *goquery.Selection) {
		if len(sources) >= limit {
			return
		}

		link := s.Find(".result__a")
		title := strings.TrimSpace(link.Text())
		href, _ := link.Attr("href")

		actualURL := extractDDGURL(href)

		snippetEl := s.Find(".result__snippet")
		snippet := strings.TrimSpace(snippetEl.Text())

		if title == "" && actualURL == "" {
			return
		}
		if title == "" {
			title = actualURL
		}

		source := SearchSource{
			Title:   title,
			URL:     actualURL,
			Snippet: snippet,
		}
		if published := extractDDGPublishedDate(s); published != "" {
			source.PublishedDate = published
			source.AgeSeconds = ageSecondsWith(ddgPublishedLayouts, published)
		}
		sources = append(sources, source)
	})

	return sources, nil
}

func extractDDGURL(href string) string {
	if href == "" {
		return ""
	}

	if strings.Contains(href, "uddg=") {
		u, err := url.Parse(href)
		if err != nil {
			return href
		}
		encoded := u.Query().Get("uddg")
		if encoded != "" {
			decoded, err := url.QueryUnescape(encoded)
			if err == nil {
				return decoded
			}
			return encoded
		}
	}

	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}

	return href
}
