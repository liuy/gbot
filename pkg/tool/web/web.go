package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/markitdown"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/web/providers"
	"github.com/liuy/gbot/pkg/tool/web/scrapers"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

const defaultLimit = 10

// Inline-image ceilings from omp fetch.ts:87-88 — a 20MB source cap
// (MAX_INLINE_IMAGE_SOURCE_BYTES) feeding the shared resize pipeline, whose
// output must fit a 300KB budget (MAX_INLINE_IMAGE_OUTPUT_BYTES). Vars, not
// consts, so boundary tests can shrink the output budget instead of forcing
// an image the ladder provably cannot compress under it.
var (
	maxInlineImageSourceBytes = int64(20 * 1024 * 1024)
	maxInlineImageOutputBytes = 300 * 1024
)

// Option configures the web tool.
type Option func(*webConfig)

type webConfig struct {
	apiKeys map[string]string // provider name → API key
}

// WithAPIKeys sets API keys for search providers.
// Empty value = anonymous mode. Omitted provider = not registered.
func WithAPIKeys(keys map[string]string) Option {
	return func(c *webConfig) { c.apiKeys = keys }
}

type Input struct {
	Query    string `json:"query"`
	Provider string `json:"provider,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Recency  string `json:"recency,omitempty"`
	JS       bool   `json:"js,omitempty"`
}

type Output struct {
	Mode    string
	Content string
	Raw     *providers.SearchResponse
	// Image carries an inline image for fetch results whose body was an
	// image. Non-nil only — presence is the signal the wire path keys on.
	Image *FetchImage
}

// New constructs the web tool with default search providers and scrapers.
// Pass nil to use http.DefaultClient.
func New(client *http.Client, opts ...Option) tool.Tool {
	if client == nil {
		client = http.DefaultClient
	}
	var cfg webConfig
	for _, o := range opts {
		o(&cfg)
	}
	chain := searchChain(client, cfg.apiKeys)
	reg := scraperRegistry()

	avail := chain.AvailableProviders()
	providerDesc := `Search provider. \"auto\" uses the first available provider.`
	if len(avail) > 0 {
		providerDesc = fmt.Sprintf(`Search provider. Available: auto, %s. Default: auto.`, strings.Join(avail, ", "))
	}

	schema := json.RawMessage(fmt.Sprintf(`{
		"type": "object",
		"required": ["query"],
		"properties": {
			"query": {
				"type": "string",
				"description": "Search query or URL to fetch. Auto-detects: URLs are fetched, other text triggers search."
			},
			"provider": {
				"type": "string",
				"description": "%s"
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of search results. Default: 10."
			},
			"recency": {
				"type": "string",
				"enum": ["day", "week", "month", "year"],
				"description": "Restrict results to this time window. Pure time filter; providers without time support ignore it."
			},
			"js": {
				"type": "boolean",
				"description": "Fetch URL with headless Chrome (JS rendering + stealth). Use when the page requires JavaScript or returns empty/blocked content."
			}
		}
	}`, providerDesc))

	return tool.BuildTool(tool.ToolDef{
		Name_:        "Web",
		Aliases_:     []string{"web"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Search the web or fetch URLs", nil
			}
			return in.Query, nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return execute(ctx, input, chain, client, reg)
		},
		IsReadOnly_: func(json.RawMessage) bool {
			return true
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			// Web is read-only HTTP; concurrent searches/fetches are safe.
			return true
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 50000,
		Prompt_:            webPrompt(),
		RenderResult_: func(data any) string {
			switch v := data.(type) {
			case *Output:
				return v.Content
			case string:
				return v
			default:
				b, _ := json.Marshal(data)
				return string(b)
			}
		},
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			if o, ok := decodeImageWire(raw); ok {
				return o, nil
			}
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var o Output
			if err := json.Unmarshal([]byte(text), &o); err != nil {
				return nil, err
			}
			// Wire text that is a bare JSON object (fetching a raw API
			// endpoint without markdown conversion) decodes into an
			// all-zero Output, which replay would render as empty instead
			// of falling back to the wire text — same false-positive class
			// the other wire-plaintext tools guard against.
			if o.Mode == "" && o.Content == "" {
				return nil, fmt.Errorf("web: decoded output lacks identifying fields (not a legacy JSON result)")
			}
			return &o, nil
		},
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			out, ok := data.(*Output)
			if !ok {
				raw, _ := json.Marshal(data)
				return []types.ContentBlock{types.NewTextBlock(string(raw))}
			}
			// Source: WebFetchTool.ts:300-306 — wire content is the processed
			// result string verbatim; Content already holds the LLM-ready text
			// from formatForLLM/BuildResult for both modes of the merged tool.
			blocks := []types.ContentBlock{types.NewTextBlock(out.Content)}
			if out.Image != nil {
				// Text first, image second — same order as Computer's
				// screenshot wire, which persist/budget already treat as a
				// skip-image unit.
				blocks = append(blocks, types.NewImageBlock(types.ImageSource{
					Type:      "base64",
					MediaType: out.Image.MimeType,
					Data:      out.Image.Base64,
				}))
			}
			return blocks
		},
		IsSearchOrRead_: func(json.RawMessage) tool.SearchReadKind {
			return tool.SearchReadKind{IsSearch: true}
		},
	})
}

func execute(ctx context.Context, input json.RawMessage, chain *providers.SearchChain, client *http.Client, reg *scrapers.Registry) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	if IsURL(in.Query) {
		return executeFetch(ctx, in.Query, in.JS, client, reg)
	}

	return executeSearch(ctx, in, chain)
}

func executeSearch(ctx context.Context, in Input, chain *providers.SearchChain) (*tool.ToolResult, error) {
	switch in.Recency {
	case "", "day", "week", "month", "year":
	default:
		return nil, fmt.Errorf("invalid recency %q (want day, week, month, or year)", in.Recency)
	}

	limit := in.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	// Parse once and hand the structured query to every provider (omp
	// executeSearch: parsedQuery is invariant across the chain).
	parsed := providers.ParseSearchQuery(in.Query)
	params := providers.SearchParams{
		Query:       in.Query,
		ParsedQuery: &parsed,
		Limit:       limit,
		Recency:     in.Recency,
	}

	resp, err := chain.SearchWithProvider(ctx, params, in.Provider)
	if err != nil {
		return nil, fmt.Errorf("web search failed: %w", err)
	}

	// Lenient constraint pass over whatever the provider returned: enforce
	// site:/inurl:/intitle:/filetype:/date directives the provider could not
	// (or only partially) honor natively, relaxing any dimension that would
	// wipe out every result. Answer text stays untouched.
	var notes []string
	if parsed.HasConstraints && len(resp.Sources) > 0 {
		filtered := providers.ApplyQueryConstraints(resp.Sources, parsed)
		if len(filtered.Sources) != len(resp.Sources) {
			resp.Sources = filtered.Sources
		}
		for _, label := range filtered.Dropped {
			notes = append(notes, fmt.Sprintf("no results matched `%s`; the constraint was relaxed", label))
		}
	}

	content := formatForLLM(resp, notes)

	return &tool.ToolResult{
		Data: &Output{
			Mode:    "search",
			Content: content,
			Raw:     resp,
		},
	}, nil
}

func extractProxyURL(client *http.Client) string {
	if client == nil || client.Transport == nil {
		return ""
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		return ""
	}
	req, err := http.NewRequest("GET", "https://example.com", nil)
	if err != nil {
		return ""
	}
	p, err := tr.Proxy(req)
	if err != nil || p == nil {
		return ""
	}
	return p.String()
}

func executeFetch(ctx context.Context, url string, js bool, client *http.Client, reg *scrapers.Registry) (*tool.ToolResult, error) {
	// Explicit JS mode: skip scrapers, go straight to chromedp.
	if js {
		slog.Info("web:fetch JS mode", "url", url)
		return fetchWithChrome(ctx, url, client)
	}

	// Try site-specific scrapers first.
	jsFetcher := func(ctx context.Context, u string) (string, error) {
		return chromedpFetch(u, 25*time.Second, extractProxyURL(client))
	}
	if reg != nil {
		parsed, err := neturl.Parse(url)
		if err == nil {
			result, err := reg.Try(ctx, parsed, client, jsFetcher)
			if err != nil {
				return nil, err
			}
			if result != nil {
				slog.Info("web:fetch scraper matched", "method", result.Method, "url", url)
				notes := result.Notes
				if notes == nil {
					notes = []string{}
				}
				fetchResult := BuildResult(result.Content, FetchResultOptions{
					URL:         url,
					FinalURL:    url,
					ContentType: result.ContentType,
					Notes:       notes,
				})
				return fetchToolResult(fetchResult), nil
			}
		}
	}

	// Binary document conversion (PDF, DOCX, PPTX, etc.).
	if md, err := fetchAndConvertDocument(ctx, url, client); err == nil && md != "" {
		slog.Info("web:fetch document converted", "url", url, "content_len", len(md))
		fetchResult := BuildResult(md, FetchResultOptions{
			URL:      url,
			FinalURL: url,
			Notes:    []string{"converted from binary document"},
		})
		return fetchToolResult(fetchResult), nil
	}

	result := LoadPage(ctx, url, LoadPageOptions{Client: client})

	if result.Error != "" {
		slog.Info("web:fetch failed", "url", url, "status", result.Status, "error", result.Error)
		return nil, fmt.Errorf("fetch failed: %s", result.Error)
	}

	slog.Info("web:fetch completed via HTTP", "url", url, "status", result.Status, "content_len", len(result.Content))

	var notes []string
	if result.Status == http.StatusNoContent {
		// omp renderUrl short-circuits 204 with exactly this note; the body
		// is empty by definition, so flowing on through the text path is
		// equivalent to its early return.
		notes = append(notes, "HTTP 204 No Content")
		// omp renderUrl short-circuits here: the body is empty by
		// definition, so flowing on would only re-fetch nothing and stack
		// image-path notes onto a 204.
		return fetchToolResult(BuildResult("", FetchResultOptions{URL: url, FinalURL: result.FinalURL, ContentType: result.ContentType, Notes: notes})), nil
	}
	if result.FinalURL != url {
		notes = append(notes, fmt.Sprintf("redirected to %s", result.FinalURL))
	}
	if result.Truncated {
		// Quoting MaxBytes is exact here: this call sets no custom MaxBytes,
		// so LoadPage enforced its default, which is the MaxBytes constant.
		// formatBytes mirrors omp's note style so the ports read alike.
		notes = append(notes, fmt.Sprintf("Response body exceeded %s and was cut mid-stream; content is incomplete", formatBytes(MaxBytes)))
	}

	if imageMime := resolveImageMimeType(result.ContentType, imageExtHint(result.FinalURL)); imageMime != "" {
		if !supportedInlineImageMime(imageMime) {
			notes = append(notes, fmt.Sprintf(
				"Image MIME type %s is unsupported for inline model serialization; returning text metadata only", imageMime))
			// omp falls back to its textual renderer here, which serves the
			// initial response's body when it is text (svg is the common
			// case); a binary body stays metadata-only so raw bytes never
			// reach the model as mojibake.
			if !textuallyDecodable(result.Content) {
				return fetchToolResult(BuildResult(
					fmt.Sprintf("Fetched image content (%s), but its MIME type is unsupported for inline model serialization.", imageMime),
					FetchResultOptions{Notes: notes})), nil
			}
			notes = append(notes, "Falling back to textual rendering from initial response")
		} else if data, err := fetchImageBinary(ctx, result.FinalURL, client); err != nil {
			// omp: explain the failure, then serve the textual rendering the
			// initial response already produced.
			notes = append(notes, fmt.Sprintf("Binary fetch failed: %v", err),
				"Falling back to textual rendering from initial response")
		} else {
			notes = append(notes, "Fetched image binary")
			return inlineImageResult(notes, imageMime, result.Content, data), nil
		}
	}

	content := result.Content
	if LooksLikeHTML(content) {
		md, err := HTMLToMarkdown(content)
		if err == nil {
			content = md
		}
	}

	fetchResult := BuildResult(content, FetchResultOptions{
		URL:         url,
		FinalURL:    result.FinalURL,
		ContentType: result.ContentType,
		Notes:       notes,
	})

	return fetchToolResult(fetchResult), nil
}

func fetchWithChrome(ctx context.Context, url string, client *http.Client) (*tool.ToolResult, error) {
	html, err := chromedpFetch(url, 20*time.Second, extractProxyURL(client))
	if err != nil {
		return nil, fmt.Errorf("JS fetch failed: %w", err)
	}

	content := html
	if LooksLikeHTML(content) {
		md, mdErr := HTMLToMarkdown(content)
		if mdErr == nil {
			content = md
		}
	}

	// BuildResult already calls FinalizeOutput internally.
	fetchResult := BuildResult(content, FetchResultOptions{
		URL:         url,
		FinalURL:    url,
		ContentType: "text/html",
		Notes:       []string{"fetched with JS rendering"},
	})

	slog.Info("web:fetch JS mode succeeded", "url", url, "content_len", len(fetchResult.Content))
	return fetchToolResult(fetchResult), nil
}

// fetchToolResult is the single assembly point for every fetch path's
// model-visible output. The render-limit trim note lives here, not at call
// sites, so no path can serve a truncated body as complete truth.
func fetchToolResult(fr FetchResult) *tool.ToolResult {
	notes := fr.Notes
	if fr.Truncated {
		notes = append(notes, "output trimmed to the render limit")
	}
	return &tool.ToolResult{
		Data: &Output{
			Mode:    "fetch",
			Content: notesLedContent(notes, fr.Content),
			Image:   fr.Image,
		},
	}
}

// inlineImageResult runs the omp fetch.ts image path (fetch.ts:1181-1260)
// over the fetched bytes: source-size gate → magic-number sniff → resize
// against the output budget → decode validation → output-size gate. Every
// outcome returns a tool result; the caller never falls through.
func inlineImageResult(notes []string, imageMime, textual string, data []byte) *tool.ToolResult {
	if int64(len(data)) > maxInlineImageSourceBytes {
		tooLargeNotes := append(notes, fmt.Sprintf(
			"Image exceeds inline source limit (%d bytes > %d bytes)", len(data), maxInlineImageSourceBytes))
		return fetchToolResult(BuildResult(
			fmt.Sprintf("Fetched image content (%s), but it is too large to inline render.", imageMime),
			FetchResultOptions{Notes: tooLargeNotes}))
	}
	sniffed := sniffedInlineImageMime(data)
	if sniffed == "" {
		return imageInvalidResult(notes, imageMime, textual, "")
	}
	resized, err := utils.MaybeResizeAndDownsampleImageBufferWithBudget(
		data, len(data), strings.TrimPrefix(sniffed, "image/"), maxInlineImageOutputBytes)
	if err != nil {
		return imageInvalidResult(notes, imageMime, textual, err.Error())
	}
	// The passthrough sub-case (Dimensions nil — DecodeConfig failed and the
	// resizer returned the original bytes) and a fast path over a
	// header-valid/body-broken payload both land here: DecodeConfig only
	// parses headers, so full-decode validation is what the API will do.
	if _, _, err := image.Decode(bytes.NewReader(resized.Buffer)); err != nil {
		return imageInvalidResult(notes, imageMime, textual, "")
	}
	if len(resized.Buffer) > maxInlineImageOutputBytes {
		outputNotes := append(notes, fmt.Sprintf(
			"Image exceeds inline output limit after resize (%d bytes > %d bytes)",
			len(resized.Buffer), maxInlineImageOutputBytes))
		return fetchToolResult(BuildResult(
			fmt.Sprintf("Fetched image content (%s), but it is too large to inline render.", imageMime),
			FetchResultOptions{Notes: outputNotes}))
	}
	// The wire MIME comes from the payload bytes, not the resizer's label:
	// the ladder re-encodes across formats (gif/webp sources become png/jpeg)
	// and a mismatched media_type is an API-side image rejection.
	mimeType := utils.DetectImageFormatFromBuffer(resized.Buffer)
	return fetchToolResult(BuildResult(
		fmt.Sprintf("Fetched image content (%s).", mimeType),
		FetchResultOptions{
			Notes: notes,
			Image: &FetchImage{
				Base64:   base64.StdEncoding.EncodeToString(resized.Buffer),
				MimeType: mimeType,
			},
		}))
}

// imageInvalidResult is omp fetch.ts:1206-1221 (image-invalid): a payload
// that claimed to be an image but would not decode degrades to the textual
// rendering (or the labeled-fallback sentence when the body is empty) plus a
// note, never an inline block. detail carries the resizer error when there
// is one; the bare note is omp's exact text.
func imageInvalidResult(notes []string, imageMime, textual, detail string) *tool.ToolResult {
	note := fmt.Sprintf("Fetched payload could not be decoded as %s; returning text metadata only", imageMime)
	if detail != "" {
		note = fmt.Sprintf("Fetched payload could not be decoded as %s: %s; returning text metadata only", imageMime, detail)
	}
	invalidNotes := append(notes, note)
	if textual == "" {
		textual = fmt.Sprintf("Fetched payload was labeled %s, but bytes were not a valid image.", imageMime)
	}
	return fetchToolResult(BuildResult(textual, FetchResultOptions{Notes: invalidNotes}))
}

// decodeImageWire unwraps the [text, image] wire form image fetches emit.
// It must run before UnmarshalSingleBlock, which returns only the first
// non-empty text block and would silently drop the image; ok=false keeps
// every legacy wire shape on that path untouched.
func decodeImageWire(raw json.RawMessage) (*Output, bool) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	var blocks []types.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, false
	}
	var out Output
	for _, b := range blocks {
		switch b.Type {
		case types.ContentTypeText:
			if b.Text != "" {
				out.Content = b.Text
			}
		case types.ContentTypeImage:
			if b.Source == nil {
				continue
			}
			out.Image = &FetchImage{Base64: b.Source.Data, MimeType: b.Source.MediaType}
		}
	}
	if out.Image == nil {
		return nil, false
	}
	out.Mode = "fetch"
	return &out, true
}

// Notes lead the model-visible content instead of riding on FetchResult fields
// the Output assembly drops: a truncated or redirected body without its note
// reads as complete truth to the model (omp: notes lead the output).
func notesLedContent(notes []string, content string) string {
	if len(notes) == 0 {
		return content
	}
	return "[note] " + strings.Join(notes, "; ") + "\n\n" + content
}

// IsConvertibleDocument returns true if the MIME type or file extension indicates
// a binary document that markitdown can convert to markdown.
func IsConvertibleDocument(mimeType, ext string) bool {
	if documentContentTypes[mimeType] {
		return true
	}
	// Explicit non-document MIME types (text/html, application/json, etc.) are never convertible.
	if mimeType != "" && mimeType != "application/octet-stream" && !documentTextMimes[mimeType] {
		return false
	}
	return documentExtensions[ext]
}

// MIME types that are technically text but should still go through markitdown
// because they need conversion (e.g. ipynb JSON → markdown).
var documentTextMimes = map[string]bool{
	"text/plain":       true,
	"application/json": true,
	"text/csv":         true,
}

var documentExtensions = map[string]bool{
	".pdf": true, ".docx": true, ".doc": true,
	".pptx": true, ".ppt": true,
	".xlsx": true, ".xls": true,
	".epub": true, ".ipynb": true,
	".csv": true,
}

var documentContentTypes = map[string]bool{
	"application/pdf": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   true,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
	"application/vnd.ms-excel": true,
	"application/epub+zip":     true,
}

// fetchAndConvertDocument detects binary document URLs and converts them to markdown.
// Returns ("", nil) if the URL is not a document type — caller should fall through to HTTP fetch.
func fetchAndConvertDocument(ctx context.Context, url string, client *http.Client) (string, error) {
	parsed, err := neturl.Parse(url)
	if err != nil {
		return "", nil
	}

	ext := strings.ToLower(getExt(parsed.Path))

	// Quick check: skip URLs that clearly aren't documents by extension.
	// URLs with matching extensions proceed directly; others need a HEAD check.
	extMatch := IsConvertibleDocument("", ext)
	if !extMatch {
		// Extension doesn't match — do a HEAD request to check Content-Type.
		if client == nil {
			client = http.DefaultClient
		}
		headReq, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
		if err != nil {
			return "", nil
		}
		headReq.Header.Set("User-Agent", "curl/8.0")
		headResp, err := client.Do(headReq)
		if err != nil {
			return "", nil
		}
		_, _ = io.Copy(io.Discard, headResp.Body)
		_ = headResp.Body.Close()

		if headResp.StatusCode < 200 || headResp.StatusCode >= 300 {
			return "", nil
		}

		ct := strings.ToLower(strings.SplitN(headResp.Header.Get("Content-Type"), ";", 2)[0])
		ct = strings.TrimSpace(ct)
		if !IsConvertibleDocument(ct, "") {
			return "", nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", nil
	}
	req.Header.Set("User-Agent", "curl/8.0")

	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil
	}

	ct := strings.ToLower(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0])
	ct = strings.TrimSpace(ct)

	// For extension-matched URLs, verify Content-Type isn't explicitly non-document.
	if extMatch && ct != "" && ct != "application/octet-stream" && !IsConvertibleDocument(ct, "") && !documentTextMimes[ct] {
		return "", nil
	}

	// Derive extension from redirect URL if original had no match.
	finalExt := ext
	if !extMatch {
		finalParsed := resp.Request.URL
		if finalExt = strings.ToLower(getExt(finalParsed.Path)); !IsConvertibleDocument("", finalExt) {
			finalExt = extByMime(ct)
		}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes))
	if err != nil {
		return "", nil
	}

	m := markitdown.New()
	result, err := m.ConvertReader(bytes.NewReader(data), markitdown.StreamInfo{
		Extension: finalExt,
		MIMEType:  ct,
		URL:       url,
	})
	if err != nil {
		return "", nil
	}

	return result.Markdown, nil
}

// extByMime returns a file extension for common document MIME types.
func extByMime(mime string) string {
	switch mime {
	case "application/pdf":
		return ".pdf"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.ms-excel":
		return ".xls"
	case "application/epub+zip":
		return ".epub"
	default:
		return ""
	}
}

// getExt returns the file extension from a URL path.
func getExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' {
			break
		}
	}
	return ""
}

// searchChain builds a search chain from config keys.
// Providers with keys configured (or supporting anonymous mode) are registered.
func searchChain(client *http.Client, keys map[string]string) *providers.SearchChain {
	var ps []providers.SearchProvider
	if _, ok := keys["anysearch"]; ok {
		ps = append(ps, &providers.AnySearchProvider{Client: client, APIKey: keys["anysearch"]})
	}
	if key, ok := keys["jina"]; ok && key != "" {
		ps = append(ps, &providers.JinaProvider{Client: client, APIKey: key})
	}
	if key, ok := keys["tavily"]; ok && key != "" {
		ps = append(ps, &providers.TavilyProvider{Client: client, APIKey: key})
	}
	if key, ok := keys["zhipu"]; ok && key != "" {
		ps = append(ps, &providers.ZhipuProvider{Client: client, APIKey: key})
	}
	// DuckDuckGo always available (no key needed). Browse wires the lazy
	// headless-Chrome escalation for blocked plain fetches.
	ps = append(ps, &providers.DuckDuckGoProvider{
		Client: client,
		Browse: func(ctx context.Context, u string, timeout time.Duration) (string, error) {
			slog.Info("web: ddg browser fallback", "url", u)
			return chromedpFetch(u, timeout, extractProxyURL(client))
		},
	})
	return &providers.SearchChain{Providers: ps}
}

// scraperRegistry returns a registry pre-populated with all built-in scrapers.
func scraperRegistry() *scrapers.Registry {
	r := scrapers.New()
	r.Register(scrapers.HandleWikipedia)
	r.Register(scrapers.HandleStackOverflow)
	r.Register(scrapers.HandleHackerNews)
	r.Register(scrapers.HandleArxiv)
	r.Register(scrapers.HandleGitHub)
	r.Register(scrapers.HandleNpm)
	r.Register(scrapers.HandlePyPI)
	r.Register(scrapers.HandleCratesIo)
	r.Register(scrapers.HandleGoPkg)
	r.Register(scrapers.HandleWeixin)
	r.Register(scrapers.HandleHuggingFace)
	return r
}

// A var, not a const, so boundary tests can shrink it instead of pushing a
// real 50 MiB body through markitdown under the race detector.
var maxDownloadBytes int64 = 50 * 1024 * 1024

// formatBytes renders sizes in omp's B/KB/MB/GB tiers (pi-utils formatBytes),
// so size notes read the same across the two ports.
func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1fGB", float64(n)/(1024*1024*1024))
	}
}
