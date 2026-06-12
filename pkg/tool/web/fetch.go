package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/JohannesKaufmann/html-to-markdown/plugin"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

const (
	MaxBytes       = 50 * 1024 * 1024
	MaxOutputChars = 500_000
)

type LoadPageOptions struct {
	Timeout  time.Duration
	Headers  map[string]string
	Method   string
	Body     io.Reader
	MaxBytes int64
	Client   *http.Client
}

type LoadPageResult struct {
	Content     string
	ContentType string
	FinalURL    string
	OK          bool
	Status      int
	Truncated   bool
	Error       string
}

var userAgents = [3]string{
	"curl/8.0",
	"Mozilla/5.0 (compatible; TextBot/1.0)",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
}

func isBotBlocked(status int, content string) bool {
	if status != http.StatusForbidden && status != http.StatusServiceUnavailable {
		return false
	}
	lower := strings.ToLower(content)
	return strings.Contains(lower, "cloudflare") ||
		strings.Contains(lower, "captcha") ||
		strings.Contains(lower, "challenge") ||
		strings.Contains(lower, "blocked") ||
		strings.Contains(lower, "access denied") ||
		strings.Contains(lower, "bot detection")
}

const retryAfterMaxMs = 10_000

func parseRetryAfterMs(value string) time.Duration {
	if value == "" {
		return time.Second
	}
	var sec int
	if _, err := fmt.Sscanf(value, "%d", &sec); err == nil && sec > 0 {
		ms := sec * 1000
		if ms > retryAfterMaxMs {
			return retryAfterMaxMs * time.Millisecond
		}
		return time.Duration(ms) * time.Millisecond
	}
	if t, err := http.ParseTime(value); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return time.Second
		}
		if d > retryAfterMaxMs*time.Millisecond {
			return retryAfterMaxMs * time.Millisecond
		}
		return d
	}
	return time.Second
}

var charsetFromCTRe = regexp.MustCompile(`charset\s*=\s*"?([\w-]+)"?`)

func charsetFromContentType(header string) string {
	m := charsetFromCTRe.FindStringSubmatch(header)
	if m == nil {
		return ""
	}
	return m[1]
}

var metaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?([\w-]+)`)

func decodeBody(data []byte, contentTypeHeader string) string {
	label := charsetFromContentType(contentTypeHeader)
	if label == "" {
		sniff := data
		if len(sniff) > 2048 {
			sniff = sniff[:2048]
		}
		// latin1 view so regex sees raw bytes, not garbled UTF-8
		latin1 := string(sniff)
		m := metaCharsetRe.FindStringSubmatch(latin1)
		if m != nil {
			label = m[1]
		}
	}

	if label != "" && !strings.EqualFold(label, "utf-8") && !strings.EqualFold(label, "utf8") {
		enc, err := htmlindex.Get(label)
		if err == nil {
			decoded, err := io.ReadAll(transform.NewReader(bytes.NewReader(data), enc.NewDecoder()))
			if err == nil {
				return string(decoded)
			}
		}
	}
	return string(data)
}

func LoadPage(ctx context.Context, url string, opts LoadPageOptions) LoadPageResult {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	maxBytes := opts.MaxBytes
	if maxBytes == 0 {
		maxBytes = MaxBytes
	}
	method := opts.Method
	if method == "" {
		method = http.MethodGet
	}

	// Buffer body so it can be rewound across retry attempts
	var bodyBuf *bytes.Reader
	if opts.Body != nil {
		bodyBytes, err := io.ReadAll(opts.Body)
		if err != nil {
			return LoadPageResult{Content: "", FinalURL: url, Error: fmt.Sprintf("read body: %v", err)}
		}
		bodyBuf = bytes.NewReader(bodyBytes)
		opts.Body = nil
	}

	var lastError string
	retried429 := false

	for attempt := 0; attempt < len(userAgents); attempt++ {
		if ctx.Err() != nil {
			return LoadPageResult{Content: "", FinalURL: url, Error: "context canceled"}
		}

		ua := userAgents[attempt]
		reqCtx, cancel := context.WithTimeout(ctx, timeout)

		result, retry429 := loadPageAttempt(ctx, reqCtx, cancel, url, ua, method, opts, maxBytes, bodyBuf, &retryState{lastError: &lastError, retried429: &retried429, attempt: attempt})
		if retry429 {
			attempt--
			continue
		}
		if result != nil {
			return *result
		}
	}

	return LoadPageResult{Content: "", FinalURL: url, Error: lastError}
}

type retryState struct {
	lastError  *string
	retried429 *bool
	attempt    int
}

func loadPageAttempt(
	ctx, reqCtx context.Context,
	cancel context.CancelFunc,
	url, ua, method string,
	opts LoadPageOptions,
	maxBytes int64,
	bodyBuf *bytes.Reader,
	rs *retryState,
) (*LoadPageResult, bool) {
	defer cancel()

	var body io.Reader
	if bodyBuf != nil {
		_, _ = bodyBuf.Seek(0, io.SeekStart)
		body = bodyBuf
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, body)
	if err != nil {
		return &LoadPageResult{Content: "", FinalURL: url, Error: err.Error()}, false
	}

	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Accept-Encoding", "identity")
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}

	client := opts.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return &LoadPageResult{Content: "", FinalURL: url, Error: "context canceled"}, false
		}
		*rs.lastError = err.Error()
		if rs.attempt == len(userAgents)-1 {
			return &LoadPageResult{Content: "", FinalURL: url, Error: *rs.lastError}, false
		}
		return nil, false
	}

	rawCT := resp.Header.Get("Content-Type")
	ct := strings.SplitN(rawCT, ";", 2)[0]
	ct = strings.TrimSpace(strings.ToLower(ct))
	finalURL := resp.Request.URL.String()

	if resp.StatusCode == http.StatusTooManyRequests && !*rs.retried429 {
		*rs.retried429 = true
		delay := parseRetryAfterMs(resp.Header.Get("Retry-After"))
		_ = resp.Body.Close()

		select {
		case <-ctx.Done():
			return &LoadPageResult{Content: "", FinalURL: url, Error: "context canceled"}, false
		case <-time.After(delay):
		}
		return nil, true
	}

	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, readErr := io.ReadAll(limited)
	_ = resp.Body.Close()

	if readErr != nil && readErr != io.EOF {
		*rs.lastError = readErr.Error()
		if rs.attempt == len(userAgents)-1 {
			return &LoadPageResult{Content: "", ContentType: ct, FinalURL: finalURL, Error: *rs.lastError}, false
		}
		return nil, false
	}

	truncated := int64(len(data)) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}

	content := decodeBody(data, rawCT)

	if isBotBlocked(resp.StatusCode, content) && rs.attempt < len(userAgents)-1 {
		return nil, false
	}

	return &LoadPageResult{
		Content:     content,
		ContentType: ct,
		FinalURL:    finalURL,
		OK:          resp.StatusCode >= 200 && resp.StatusCode < 300,
		Status:      resp.StatusCode,
		Truncated:   truncated,
	}, false
}

var (
	multiNewlineRe = regexp.MustCompile(`\n{3,}`)
	scriptRe       = regexp.MustCompile(`(?i)<script[\s\S]*?</script>`)
	styleRe        = regexp.MustCompile(`(?i)<style[\s\S]*?</style>`)
)

func FinalizeOutput(content string) (string, bool) {
	cleaned := multiNewlineRe.ReplaceAllString(content, "\n\n")
	cleaned = strings.TrimSpace(cleaned)
	truncated := len(cleaned) > MaxOutputChars
	if truncated {
		cleaned = truncateStringToRunes(cleaned, MaxOutputChars)
	}
	return cleaned, truncated
}

func truncateStringToRunes(s string, maxRunes int) string {
	count := 0
	for i := range s {
		count++
		if count > maxRunes {
			return s[:i]
		}
	}
	return s
}

func LooksLikeHTML(content string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(content))
	return strings.HasPrefix(trimmed, "<!doctype") ||
		strings.HasPrefix(trimmed, "<html") ||
		strings.HasPrefix(trimmed, "<head") ||
		strings.HasPrefix(trimmed, "<body")
}

func DecodeHTMLEntities(text string) string {
	replacements := []struct{ from, to string }{
		{"&lt;", "<"},
		{"&gt;", ">"},
		{"&amp;", "&"},
		{"&quot;", `"`},
		{"&#039;", "'"},
		{"&#39;", "'"},
		{"&#x27;", "'"},
		{"&#x2F;", "/"},
		{"&nbsp;", " "},
	}
	for _, r := range replacements {
		text = strings.ReplaceAll(text, r.from, r.to)
	}
	return text
}

func stripScriptsAndStyles(htmlStr string) string {
	s := scriptRe.ReplaceAllString(htmlStr, "")
	s = styleRe.ReplaceAllString(s, "")
	return s
}

var markdownConverter *md.Converter

func init() {
	markdownConverter = md.NewConverter("", true, &md.Options{
		HeadingStyle:     "atx",
		CodeBlockStyle:   "fenced",
		BulletListMarker: "-",
	})
	markdownConverter.Use(plugin.GitHubFlavored())

	// omp's Turndown escapes leading dots in headings; strip those.
	markdownConverter.AddRules(md.Rule{
		Filter: []string{"h1", "h2", "h3", "h4", "h5", "h6"},
		Replacement: func(content string, selec *goquery.Selection, options *md.Options) *string {
			node := selec.Get(0)
			level := 1
			if node != nil {
				switch node.Data {
				case "h2":
					level = 2
				case "h3":
					level = 3
				case "h4":
					level = 4
				case "h5":
					level = 5
				case "h6":
					level = 6
				}
			}
			prefix := strings.Repeat("#", level)
			cleaned := strings.ReplaceAll(content, `\.`, ".")
			cleaned = strings.TrimSpace(cleaned)
			result := fmt.Sprintf("\n\n%s %s\n\n", prefix, cleaned)
			return &result
		},
	})
}

func HTMLToMarkdown(htmlStr string) (string, error) {
	cleaned := stripScriptsAndStyles(htmlStr)
	result, err := markdownConverter.ConvertString(cleaned)
	if err != nil {
		return "", fmt.Errorf("html→markdown: %w", err)
	}
	return strings.TrimSpace(result), nil
}

func FormatISODate(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		if len(v) >= 10 && v[4] == '-' && v[7] == '-' {
			return v[:10]
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.Format("2006-01-02")
			}
		}
	case float64:
		sec := int64(v)
		if sec > 1e9 {
			return time.Unix(sec, 0).Format("2006-01-02")
		}
	case int:
		return time.Unix(int64(v), 0).Format("2006-01-02")
	case int64:
		return time.Unix(v, 0).Format("2006-01-02")
	}
	return ""
}

func FormatMediaDuration(totalSeconds float64) string {
	hours := int(totalSeconds) / 3600
	minutes := (int(totalSeconds) % 3600) / 60
	secs := int(totalSeconds) % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%d:%02d", minutes, secs)
}

type FetchResultOptions struct {
	URL         string
	FinalURL    string
	Method      string
	FetchedAt   string
	Notes       []string
	ContentType string
}

func BuildResult(mdContent string, opts FetchResultOptions) FetchResult {
	content, truncated := FinalizeOutput(mdContent)
	finalURL := opts.FinalURL
	if finalURL == "" {
		finalURL = opts.URL
	}
	ct := opts.ContentType
	if ct == "" {
		ct = "text/markdown"
	}
	notes := opts.Notes
	if notes == nil {
		notes = []string{}
	}
	return FetchResult{
		URL:         opts.URL,
		FinalURL:    finalURL,
		ContentType: ct,
		Method:      opts.Method,
		Content:     content,
		FetchedAt:   opts.FetchedAt,
		Truncated:   truncated,
		Notes:       notes,
	}
}

type FetchResult struct {
	URL         string
	FinalURL    string
	ContentType string
	Method      string
	Content     string
	FetchedAt   string
	Truncated   bool
	Notes       []string
}

// ExtractTextFromHTML skips full markdown conversion — used by scrapers
// that only need raw text and don't care about formatting.
func ExtractTextFromHTML(htmlStr string) string {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return htmlStr
	}
	var buf strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			buf.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return strings.TrimSpace(buf.String())
}
