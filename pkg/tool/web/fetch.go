package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
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
	// Unreachable in practice (every final-attempt path returns above) but
	// gc does not treat a bounded for as a terminating statement.
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
	ct, _, _ := strings.Cut(rawCT, ";")
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

var markdownConverter *converter.Converter

func init() {
	markdownConverter = converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(
				commonmark.WithHeadingStyle("atx"),
			),
			strikethrough.NewStrikethroughPlugin(),
			table.NewTablePlugin(),
		),
	)

	// Strip escaped leading dots in headings (carried over from v1 custom rule).
	for _, tag := range []string{"h1", "h2", "h3", "h4", "h5", "h6"} {
		markdownConverter.Register.RendererFor(tag, converter.TagTypeBlock, func(ctx converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
			level := 1
			switch n.Data {
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
			prefix := strings.Repeat("#", level)
			_, _ = w.WriteRune('\n')
			_, _ = w.WriteString(prefix)
			_, _ = w.WriteRune(' ')
			return converter.RenderTryNext
		}, 0)
	}
}

func HTMLToMarkdown(htmlStr string) (string, error) {
	result, err := markdownConverter.ConvertString(htmlStr)
	if err != nil {
		return "", fmt.Errorf("html→markdown: %w", err)
	}
	return strings.TrimSpace(result), nil
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
	Image       *FetchImage
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
		Image:       opts.Image,
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
	Image       *FetchImage
}

// FetchImage mirrors omp FetchRenderResult.image: the base64 payload that
// rides alongside the text summary into the model's tool_result blocks.
type FetchImage struct {
	Base64   string
	MimeType string
}

// imageMimeByExtension is the URL-extension half of the image decision
// (omp IMAGE_MIME_BY_EXTENSION).
var imageMimeByExtension = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// resolveImageMimeType decides whether a response is an image: an image/*
// Content-Type wins; only generic binary labels (empty, octet-stream,
// unknown) may trust the URL extension, so an explicit text/html or
// application/pdf label is never overridden into the image path.
// Source: omp fetch.ts resolveImageMimeType.
func resolveImageMimeType(mime, extHint string) string {
	if strings.HasPrefix(mime, "image/") {
		return mime
	}
	switch mime {
	case "", "application/octet-stream", "binary/octet-stream", "unknown":
		return imageMimeByExtension[extHint]
	}
	return ""
}

// supportedInlineImageMime is the set the model serializes inline (omp
// SUPPORTED_INLINE_IMAGE_MIME_TYPES); svg/bmp/tiff and friends degrade to
// text metadata instead of an invalid image block.
func supportedInlineImageMime(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// imageExtHint is the extension hint for the image decision, taken from the
// final (post-redirect) URL path and lowercased like omp's
// getFilenameExtensionHint.
func imageExtHint(finalURL string) string {
	parsed, err := neturl.Parse(finalURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(getExt(parsed.Path))
}

// sniffedInlineImageMime reports the byte-truthful image MIME for the inline
// path: http.DetectContentType (which considers at most the first 512 bytes
// itself), accepted only when the magic bytes are exactly one of the four
// inline-serializable image types. A lying Content-Type header (HTML served
// as image/png) or an empty body sniffs as text/* or application/* and fails
// here, steering the fetch onto the text-metadata path before a bad block
// can reach the API.
func sniffedInlineImageMime(data []byte) string {
	mime, _, _ := strings.Cut(http.DetectContentType(data), ";")
	if supportedInlineImageMime(mime) {
		return mime
	}
	return ""
}

// textuallyDecodable reports whether an unsupported-image body is text (svg
// is the common case) that can be served to the model verbatim. Binary
// bodies would render as mojibake and stay on the metadata-only path. The
// manual 512-byte cap bounds the string→[]byte copy; DetectContentType would
// apply the same cap after a full-buffer conversion.
func textuallyDecodable(content string) bool {
	sniff := content
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	mime, _, _ := strings.Cut(http.DetectContentType([]byte(sniff)), ";")
	return strings.HasPrefix(mime, "text/")
}

// fetchImageBinary re-downloads the URL as raw bytes for the inline image
// path (omp renderUrl re-fetches via fetchBinary instead of reusing the
// page body). LoadPage's Content is not byte-truthful for binaries: a
// charset label on the response would route the payload through a text
// decoder before this code ever sees it.
func fetchImageBinary(ctx context.Context, pageURL string, client *http.Client) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; TextBot/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// MaxBytes, not the much smaller inline cap: the too-large note reports
	// the real byte count, same as omp reading up to its MAX_BYTES before
	// judging the size.
	return io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
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
