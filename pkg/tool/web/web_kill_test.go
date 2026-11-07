package web

// chromedpFetch is stubbed where tests need its call arguments; the stub is
// always restored via defer.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/markitdown"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/web/providers"
	"github.com/liuy/gbot/pkg/tool/web/scrapers"
)

func mustTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../markitdown/testdata/" + name)
	if err != nil {
		t.Skipf("testdata %s unavailable: %v", name, err)
	}
	return data
}

const docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

func TestWKToolDefProperties(t *testing.T) {
	tt := New(nil)
	if tt.Name() != "Web" {
		t.Errorf("Name() = %q, want Web", tt.Name())
	}
	if got := tt.Aliases(); len(got) != 1 || got[0] != "web" {
		t.Errorf("Aliases() = %v, want [web]", got)
	}
	// Literal 50000, not a shared constant: mutating the field must not
	// move the pin with it.
	if tt.MaxResultSize() != 50000 {
		t.Errorf("MaxResultSize() = %d, want 50000", tt.MaxResultSize())
	}
	if !tt.IsReadOnly(nil) {
		t.Error("IsReadOnly() = false, want true")
	}
	if !tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() = false, want true")
	}
	if tt.IsDestructive(nil) {
		t.Error("IsDestructive() = true, want false")
	}
	if tt.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %v, want InterruptCancel", tt.InterruptBehavior())
	}
	if p := tt.Prompt(); p == "" || !strings.Contains(p, "search") {
		t.Errorf("Prompt() = %q, want non-empty mentioning search", p)
	}
	schema := tt.InputSchema()
	if !json.Valid(schema) || !bytes.Contains(schema, []byte(`"query"`)) {
		t.Errorf("InputSchema() invalid or missing query: %s", schema)
	}
	got, err := tt.Description(json.RawMessage(`{"query":"find go docs"}`))
	if err != nil || got != "find go docs" {
		t.Errorf("Description(query) = %q, %v; want find go docs", got, err)
	}
	got, err = tt.Description(json.RawMessage(`{bad json`))
	if err != nil || got != "Search the web or fetch URLs" {
		t.Errorf("Description(bad) = %q, %v; want fallback text", got, err)
	}
	if s := tt.RenderResult(&Output{Content: "C"}); s != "C" {
		t.Errorf("RenderResult(*Output) = %q, want C", s)
	}
	if s := tt.RenderResult("raw"); s != "raw" {
		t.Errorf("RenderResult(string) = %q, want raw", s)
	}
	if s := tt.RenderResult(3); s != "3" {
		t.Errorf("RenderResult(int) = %q, want 3", s)
	}
	wb, ok := tt.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Web tool must implement ToolWithWireBlocks")
	}
	blocks := wb.FormatWireBlocks(&Output{Content: "W"})
	if len(blocks) != 1 || blocks[0].Text != "W" {
		t.Errorf("FormatWireBlocks(*Output) = %v, want single W block", blocks)
	}
}

func TestWKExecuteParseInputWrap(t *testing.T) {
	_, err := execute(context.Background(), json.RawMessage(`{nope`), &providers.SearchChain{}, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON input")
	}
	if !strings.HasPrefix(err.Error(), "parse input: ") {
		t.Errorf("error = %q, want 'parse input: ' prefix", err.Error())
	}
	if errors.Unwrap(err) == nil {
		t.Error("error must wrap the json error (%w)")
	}
}

func TestWKExecuteSearchLimitBoundary(t *testing.T) {
	run := func(limit int) int {
		mp := &mockProvider{id: "test", available: true, resp: &providers.SearchResponse{Provider: "test"}}
		chain := &providers.SearchChain{Providers: []providers.SearchProvider{mp}}
		raw := fmt.Sprintf(`{"query": "go", "limit": %d}`, limit)
		if _, err := execute(context.Background(), json.RawMessage(raw), chain, nil, nil); err != nil {
			t.Fatalf("execute(limit=%d) error: %v", limit, err)
		}
		if mp.recorded == nil {
			t.Fatal("provider not called")
		}
		return mp.recorded.Limit
	}
	// limit 1 must stay 1 (boundary of the <=0 default), and 0 defaults to
	// a literal 10.
	if got := run(1); got != 1 {
		t.Errorf("limit 1 forwarded as %d, want 1", got)
	}
	if got := run(0); got != 10 {
		t.Errorf("limit 0 defaulted to %d, want 10", got)
	}
}

func TestWKExecuteSearchErrorWrap(t *testing.T) {
	mp := &mockProvider{id: "test", available: true, err: errors.New("provider down")}
	chain := &providers.SearchChain{Providers: []providers.SearchProvider{mp}}
	_, err := execute(context.Background(), json.RawMessage(`{"query": "go"}`), chain, nil, nil)
	if err == nil {
		t.Fatal("expected error when search fails")
	}
	if !strings.HasPrefix(err.Error(), "web search failed: ") {
		t.Errorf("error = %q, want 'web search failed: ' prefix", err.Error())
	}
	if errors.Unwrap(err) == nil {
		t.Error("error must wrap the provider error (%w)")
	}
}

func TestWKSearchChainProviders(t *testing.T) {
	client := &http.Client{}
	chain := searchChain(client, map[string]string{
		"anysearch": "ak",
		"zhipu":     "zk",
	})
	var any, zhipu, ddg int
	for _, p := range chain.Providers {
		switch v := p.(type) {
		case *providers.AnySearchProvider:
			any++
			if v.Client != client {
				t.Error("AnySearchProvider.Client not wired")
			}
			if v.APIKey != "ak" {
				t.Errorf("AnySearchProvider.APIKey = %q, want ak", v.APIKey)
			}
		case *providers.ZhipuProvider:
			zhipu++
			if v.Client != client {
				t.Error("ZhipuProvider.Client not wired")
			}
			if v.APIKey != "zk" {
				t.Errorf("ZhipuProvider.APIKey = %q, want zk", v.APIKey)
			}
		case *providers.DuckDuckGoProvider:
			ddg++
		}
	}
	if any != 1 {
		t.Errorf("AnySearchProvider count = %d, want 1", any)
	}
	if zhipu != 1 {
		t.Errorf("ZhipuProvider count = %d, want 1", zhipu)
	}
	if ddg != 1 {
		t.Errorf("DuckDuckGoProvider count = %d, want 1", ddg)
	}

	// An empty zhipu key must not register the provider at all.
	emptyChain := searchChain(client, map[string]string{"zhipu": ""})
	for _, p := range emptyChain.Providers {
		if _, ok := p.(*providers.ZhipuProvider); ok {
			t.Error("ZhipuProvider registered with empty key, want skipped")
		}
	}
}

func TestWKDDGBrowseCtx(t *testing.T) {
	chain := searchChain(nil, nil)
	ddg, ok := chain.Providers[len(chain.Providers)-1].(*providers.DuckDuckGoProvider)
	if !ok {
		t.Fatalf("last provider is %T, want DuckDuckGoProvider", chain.Providers[len(chain.Providers)-1])
	}
	if ddg.Browse == nil {
		t.Fatal("DuckDuckGoProvider.Browse not wired")
	}

	got := make(chan time.Duration, 1)
	restore := stubChromedpFetch(func(url string, timeout time.Duration, proxy string) (string, error) {
		got <- timeout
		return "<html></html>", nil
	})
	defer restore()

	if _, err := ddg.Browse(context.Background(), "http://example.com/", 3*time.Second); err != nil {
		t.Fatalf("Browse error: %v", err)
	}
	if c := <-got; c != 3*time.Second {
		t.Errorf("chromedpFetch timeout = %v, want 3s", c)
	}
}

func stubChromedpFetch(f func(url string, timeout time.Duration, proxy string) (string, error)) func() {
	orig := chromedpFetch
	chromedpFetch = f
	return func() { chromedpFetch = orig }
}

func TestWKJSModeStub(t *testing.T) {
	type call struct {
		url     string
		timeout time.Duration
		proxy   string
	}
	got := make(chan call, 1)
	restore := stubChromedpFetch(func(url string, timeout time.Duration, proxy string) (string, error) {
		got <- call{url, timeout, proxy}
		return "<html><body><h1>Hi</h1></body></html>", nil
	})
	defer restore()

	ctx := context.Background()
	input := json.RawMessage(`{"query": "https://example.com/page", "js": true}`)
	result, err := execute(ctx, input, &providers.SearchChain{}, nil, nil)
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	output := result.Data.(*Output)
	if output.Mode != "fetch" {
		t.Errorf("mode = %q, want fetch", output.Mode)
	}
	if !strings.Contains(output.Content, "# Hi") || strings.Contains(output.Content, "<h1>") {
		t.Errorf("JS content not converted to markdown: %q", output.Content)
	}
	c := <-got
	if c.url != "https://example.com/page" {
		t.Errorf("chromedpFetch url = %q", c.url)
	}
	// JS mode pins its own 20s deadline.
	if c.timeout != 20*time.Second {
		t.Errorf("chromedpFetch timeout = %v, want 20s", c.timeout)
	}
	if c.proxy != "" {
		t.Errorf("chromedpFetch proxy = %q, want empty for nil client", c.proxy)
	}

	// Failure path must wrap with %w so callers can unwrap.
	restoreErr := stubChromedpFetch(func(string, time.Duration, string) (string, error) {
		return "", errors.New("chrome exploded")
	})
	defer restoreErr()
	_, err = execute(context.Background(), input, &providers.SearchChain{}, nil, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "JS fetch failed: ") {
		t.Fatalf("error = %v, want 'JS fetch failed: ' prefix", err)
	}
	if errors.Unwrap(err) == nil {
		t.Error("JS fetch error must wrap with %w")
	}
}

func TestWKScraperJSFetcherWiring(t *testing.T) {
	type jsCall struct {
		url     string
		timeout time.Duration
	}
	jsCalls := make(chan jsCall, 4)
	var handlerCtx context.Context
	reg := scrapers.New()
	reg.Register(func(ctx context.Context, u *neturl.URL, client *http.Client, js scrapers.JSFetcher) (*scrapers.Result, error) {
		handlerCtx = ctx
		if js == nil {
			return nil, fmt.Errorf("js fetcher not wired")
		}
		if _, err := js(ctx, "http://render.example/item"); err != nil {
			return nil, err
		}
		return &scrapers.Result{Content: "scraped", Method: "test"}, nil
	})
	restore := stubChromedpFetch(func(u string, timeout time.Duration, proxy string) (string, error) {
		jsCalls <- jsCall{u, timeout}
		return "<html></html>", nil
	})
	defer restore()

	ctx := context.Background()
	input := json.RawMessage(`{"query": "https://example.com/thing"}`)
	result, err := execute(ctx, input, &providers.SearchChain{}, nil, reg)
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if output := result.Data.(*Output); output.Content != "scraped" {
		t.Errorf("content = %q, want scraped", output.Content)
	}
	if handlerCtx != ctx {
		t.Error("reg.Try must forward the caller ctx to handlers")
	}
	select {
	case c := <-jsCalls:
		if c.url != "http://render.example/item" {
			t.Errorf("jsFetcher url = %q", c.url)
		}
		// Scraper fallback renders get 25s, distinct from JS mode's 20s.
		if c.timeout != 25*time.Second {
			t.Errorf("jsFetcher timeout = %v, want 25s", c.timeout)
		}
	default:
		t.Fatal("js fetcher never reached chromedpFetch")
	}
}

func TestWKScraperRegistryUsable(t *testing.T) {
	// A registry with no matching handler returns (nil, nil); a nil
	// registry (mutant return) would panic on Try.
	reg := scraperRegistry()
	parsed, err := neturl.Parse("https://example.com/not-a-known-site")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := reg.Try(context.Background(), parsed, http.DefaultClient, nil)
	if err != nil {
		t.Fatalf("Try error: %v", err)
	}
	if res != nil {
		t.Errorf("Try = %+v, want nil for unmatched URL", res)
	}
}

func TestWKIsConvertibleDocumentMaps(t *testing.T) {
	for _, ext := range []string{".pdf", ".docx", ".doc", ".pptx", ".ppt", ".xlsx", ".xls", ".epub", ".ipynb", ".csv"} {
		if !IsConvertibleDocument("", ext) {
			t.Errorf("IsConvertibleDocument(\"\", %q) = false, want true", ext)
		}
	}
	for _, mime := range []string{
		"application/pdf",
		docxMIME,
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.ms-excel",
		"application/epub+zip",
	} {
		if !IsConvertibleDocument(mime, "") {
			t.Errorf("IsConvertibleDocument(%q, \"\") = false, want true", mime)
		}
	}
	// Text mimes still allow extension-based conversion.
	for _, mime := range []string{"text/plain", "application/json", "text/csv"} {
		if !IsConvertibleDocument(mime, ".pdf") {
			t.Errorf("IsConvertibleDocument(%q, \".pdf\") = false, want true", mime)
		}
	}
}

// methodServer serves per-method status/content so status-window and
// content-type gates can be probed without network access.
type methodServer struct {
	*httptest.Server
	headStatus int
	getStatus  int
	headCT     string
	getCT      string
	body       []byte
	headCalls  int
	getCalls   int
	headUA     string
	getUA      string
}

func newMethodServer(t *testing.T) *methodServer {
	t.Helper()
	ms := &methodServer{headStatus: 200, getStatus: 200}
	ms.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			ms.headCalls++
			ms.headUA = r.Header.Get("User-Agent")
			if ms.headCT != "" {
				w.Header().Set("Content-Type", ms.headCT)
			}
			w.WriteHeader(ms.headStatus)
		default:
			ms.getCalls++
			ms.getUA = r.Header.Get("User-Agent")
			if ms.getCT != "" {
				w.Header().Set("Content-Type", ms.getCT)
			}
			w.WriteHeader(ms.getStatus)
			_, _ = w.Write(ms.body)
		}
	}))
	t.Cleanup(ms.Close)
	return ms
}

// rtMethodClient answers canned per-method responses; RT-level responses
// pass 1xx statuses through deterministically, unlike a real listener.
func rtMethodClient(headStatus, getStatus int, headCT, getCT string, body []byte) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hdr := http.Header{}
		ct := getCT
		status := getStatus
		if r.Method == http.MethodHead {
			ct = headCT
			status = headStatus
		}
		if ct != "" {
			hdr.Set("Content-Type", ct)
		}
		return &http.Response{
			Status:     fmt.Sprintf("%d canned", status),
			StatusCode: status,
			Header:     hdr,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    r,
		}, nil
	})}
}

func TestWKFACDHeadStatusWindow(t *testing.T) {
	docx := mustTestdata(t, "test.docx")
	for _, tc := range []struct {
		headStatus int
		wantMD     bool
	}{
		{199, false},
		{200, true},
		{299, true},
		{300, false},
	} {
		client := rtMethodClient(tc.headStatus, 200, docxMIME, docxMIME, docx)
		md, err := fetchAndConvertDocument(context.Background(), "http://canned.example/file", client)
		if err != nil {
			t.Fatalf("head %d: error %v", tc.headStatus, err)
		}
		if tc.wantMD && md == "" {
			t.Errorf("head %d: expected conversion, got empty", tc.headStatus)
		}
		if !tc.wantMD && md != "" {
			t.Errorf("head %d: expected no conversion, got %d chars", tc.headStatus, len(md))
		}
	}
}

func TestWKFACDHeadUserAgent(t *testing.T) {
	docx := mustTestdata(t, "test.docx")
	ms := newMethodServer(t)
	ms.headCT = docxMIME
	ms.getCT = docxMIME
	ms.body = docx

	if md, _ := fetchAndConvertDocument(context.Background(), ms.URL+"/file", ms.Client()); md == "" {
		t.Fatal("expected conversion")
	}
	if ms.headUA != "curl/8.0" {
		t.Errorf("HEAD User-Agent = %q, want curl/8.0", ms.headUA)
	}
	if ms.getUA != "curl/8.0" {
		t.Errorf("GET User-Agent = %q, want curl/8.0", ms.getUA)
	}
	if ms.headCalls != 1 {
		t.Errorf("HEAD calls = %d, want 1", ms.headCalls)
	}
}

func TestWKFACDGetStatusWindow(t *testing.T) {
	docx := mustTestdata(t, "test.docx")
	for _, tc := range []struct {
		getStatus int
		wantMD    bool
	}{
		{199, false},
		{200, true},
		{299, true},
		{300, false},
		{404, false},
	} {
		client := rtMethodClient(200, tc.getStatus, docxMIME, docxMIME, docx)
		md, err := fetchAndConvertDocument(context.Background(), "http://canned.example/file", client)
		if err != nil {
			t.Fatalf("get %d: error %v", tc.getStatus, err)
		}
		if tc.wantMD && md == "" {
			t.Errorf("get %d: expected conversion, got empty", tc.getStatus)
		}
		if !tc.wantMD && md != "" {
			t.Errorf("get %d: expected no conversion, got %d chars", tc.getStatus, len(md))
		}
	}
}

func TestWKFACDContentTypeParams(t *testing.T) {
	docx := mustTestdata(t, "test.docx")

	// Parameterized HEAD Content-Type must still be split on ';'.
	ms := newMethodServer(t)
	ms.headCT = docxMIME + "; charset=binary"
	ms.getCT = docxMIME
	ms.body = docx
	if md, _ := fetchAndConvertDocument(context.Background(), ms.URL+"/file", ms.Client()); md == "" {
		t.Error("parameterized HEAD content type: expected conversion")
	}

	// Spaced + parameterized GET Content-Type must be trimmed and split.
	ms = newMethodServer(t)
	ms.headCT = docxMIME
	ms.getCT = " " + docxMIME + " ; version=1"
	ms.body = docx
	if md, _ := fetchAndConvertDocument(context.Background(), ms.URL+"/file", ms.Client()); md == "" {
		t.Error("spaced GET content type: expected conversion")
	}

	// Spaced HEAD Content-Type must be trimmed before the HEAD gate.
	ms = newMethodServer(t)
	ms.headCT = " application/pdf "
	ms.getCT = "application/pdf"
	ms.body = mustTestdata(t, "test.pdf")
	if md, _ := fetchAndConvertDocument(context.Background(), ms.URL+"/file", ms.Client()); md == "" {
		t.Error("spaced HEAD content type: expected conversion")
	}
}

func TestWKFACDNilClientGETPath(t *testing.T) {
	// A nil client on the extension-match path must fall back to the
	// default client for the GET (the HEAD gate is skipped entirely).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = fmt.Fprint(w, "a,b\n1,2\n")
	}))
	defer server.Close()

	md, err := fetchAndConvertDocument(context.Background(), server.URL+"/f.csv", nil)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if md == "" {
		t.Error("nil client: expected conversion via default client")
	}
}

func TestWKFACDExtMatchGate(t *testing.T) {
	docx := mustTestdata(t, "test.docx")

	// Extension-matched URL with an explicitly non-document GET type is
	// rejected.
	ms := newMethodServer(t)
	ms.getCT = "text/html"
	ms.body = docx
	if md, err := fetchAndConvertDocument(context.Background(), ms.URL+"/f.docx", ms.Client()); md != "" || err != nil {
		t.Errorf("html content type on .docx URL: got (%d chars, %v), want empty", len(md), err)
	}
	if ms.headCalls != 0 {
		t.Errorf("extMatch must skip HEAD, got %d calls", ms.headCalls)
	}

	// text/csv is a convertible text mime: .csv URL converts.
	ms = newMethodServer(t)
	ms.getCT = "text/plain"
	ms.body = []byte("a,b\n1,2\n")
	if md, _ := fetchAndConvertDocument(context.Background(), ms.URL+"/f.csv", ms.Client()); md == "" {
		t.Error("text/plain on .csv URL: expected conversion")
	}

	// No Content-Type at all: the extension alone drives conversion.
	ms = newMethodServer(t)
	ms.getCT = ""
	ms.body = []byte("a,b\n1,2\n")
	if md, _ := fetchAndConvertDocument(context.Background(), ms.URL+"/f.csv", ms.Client()); md == "" {
		t.Error("missing content type on .csv URL: expected conversion")
	}

	// octet-stream never blocks an extension match.
	ms = newMethodServer(t)
	ms.getCT = "application/octet-stream"
	ms.body = docx
	if md, _ := fetchAndConvertDocument(context.Background(), ms.URL+"/f.docx", ms.Client()); md == "" {
		t.Error("octet-stream on .docx URL: expected conversion")
	}

	// A convertible document type on a matching extension converts (the
	// gate only exists for explicitly non-document types).
	ms = newMethodServer(t)
	ms.getCT = docxMIME
	ms.body = docx
	if md, _ := fetchAndConvertDocument(context.Background(), ms.URL+"/f.docx", ms.Client()); md == "" {
		t.Error("docx content type on .docx URL: expected conversion")
	}

	// A truly absent Content-Type (real servers sniff one; a canned
	// transport does not) still converts by extension.
	rtNone := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "200 canned",
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader([]byte("a,b\n1,2\n"))),
			Request:    r,
		}, nil
	})}
	if md, _ := fetchAndConvertDocument(context.Background(), "http://canned.example/f.csv", rtNone); md == "" {
		t.Error("absent content type on .csv URL: expected conversion")
	}
}

func TestWKFACDRedirectExtDerivation(t *testing.T) {
	docx := mustTestdata(t, "test.docx")
	pdf := mustTestdata(t, "test.pdf")

	// No-ext URL redirecting to .docx: the redirect extension wins.
	ms := newMethodServer(t)
	ms.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Type", "application/pdf")
			w.WriteHeader(200)
		default:
			if r.URL.Path == "/doc" {
				http.Redirect(w, r, "/file.docx", http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.WriteHeader(200)
			_, _ = w.Write(docx)
		}
	})
	if md, err := fetchAndConvertDocument(context.Background(), ms.URL+"/doc", ms.Client()); md == "" || err != nil {
		t.Errorf("redirect to .docx: got (%d chars, %v), want conversion", len(md), err)
	}

	// No-ext URL redirecting to .txt: extension can't drive conversion, so
	// the MIME-derived extension (.pdf) must. Compare against a direct
	// conversion with the same StreamInfo.
	m := markitdown.New()
	ref, refErr := m.ConvertReader(bytes.NewReader(pdf), markitdown.StreamInfo{Extension: ".pdf", MIMEType: "application/pdf"})
	if refErr != nil {
		t.Fatalf("reference conversion: %v", refErr)
	}
	ms = newMethodServer(t)
	ms.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Type", "application/pdf")
			w.WriteHeader(200)
		default:
			if r.URL.Path == "/doc" {
				http.Redirect(w, r, "/file.txt", http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.WriteHeader(200)
			_, _ = w.Write(pdf)
		}
	})
	md, err := fetchAndConvertDocument(context.Background(), ms.URL+"/doc", ms.Client())
	if err != nil {
		t.Fatalf("redirect to .txt: error %v", err)
	}
	if md != ref.Markdown {
		t.Errorf("redirect to .txt: md (%d chars) != reference pdf conversion (%d chars)", len(md), len(ref.Markdown))
	}

	// Redirect to .txt with a pdf Content-Type over csv bytes: the
	// mime-derived .pdf extension makes every converter fail, so only a
	// wrongly-kept ".txt" extension (plaintext fallback) would convert.
	ms = newMethodServer(t)
	ms.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Type", "application/pdf")
			w.WriteHeader(200)
		default:
			if r.URL.Path == "/doc" {
				http.Redirect(w, r, "/file.txt", http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("a,b\n1,2\n"))
		}
	})
	if md, err := fetchAndConvertDocument(context.Background(), ms.URL+"/doc", ms.Client()); md != "" || err != nil {
		t.Errorf("redirect to .txt with pdf type: got (%d chars, %v), want empty", len(md), err)
	}
}

func TestWKFACDStreamInfoFields(t *testing.T) {
	docx := mustTestdata(t, "test.docx")

	// With the MIME stuck at octet-stream, only the extension signal
	// drives the docx converter.
	ms := newMethodServer(t)
	ms.getCT = "application/octet-stream"
	ms.body = docx
	if md, _ := fetchAndConvertDocument(context.Background(), ms.URL+"/f.docx", ms.Client()); md == "" {
		t.Error("docx via extension + octet-stream: expected conversion")
	}
}

func TestWKDecodeResultBadBlock(t *testing.T) {
	tt := New(nil)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(json.RawMessage(`"flat string"`))
	if err == nil {
		t.Fatal("expected error for non-array wire content")
	}
	if !strings.Contains(err.Error(), "UnmarshalSingleBlock expects array-form") {
		t.Errorf("error = %q, want UnmarshalSingleBlock guard error", err.Error())
	}
}

func TestWKFACDReadBodyError(t *testing.T) {
	// Content-Length lie makes ReadAll fail mid-body; the function must
	// return ("", nil) even though the partial body would convert. The
	// .csv URL skips the HEAD gate so the GET path is exercised.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			t.Errorf("HEAD must be skipped for extension matches")
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", 500)
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(bufrw, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: text/csv\r\n\r\na,b\n1,2")
		_ = bufrw.Flush()
	}))
	defer server.Close()

	md, err := fetchAndConvertDocument(context.Background(), server.URL+"/f.csv", server.Client())
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if md != "" {
		t.Errorf("md = %d chars, want empty on body read error", len(md))
	}
}

func TestWKFACDLimit50MiB(t *testing.T) {
	// The cap is pinned as the exact literal product; the boundary itself
	// runs at a shrunken cap (restored on cleanup) so the suite never pushes
	// a real 50 MiB body through markitdown.
	if maxDownloadBytes != 50*1024*1024 {
		t.Fatalf("maxDownloadBytes = %d, want 50*1024*1024", maxDownloadBytes)
	}
	saved := maxDownloadBytes
	maxDownloadBytes = 2048
	t.Cleanup(func() { maxDownloadBytes = saved })
	mkBody := func(total int) []byte {
		prefix := `{"cells":[{"cell_type":"markdown","source":"`
		suffix := `ZZZ_END"}]}`
		pad := total - len(prefix) - len(suffix)
		if pad < 0 {
			panic("body too small")
		}
		return []byte(prefix + strings.Repeat("A", pad) + suffix)
	}

	ms := newMethodServer(t)
	ms.getCT = "application/json"
	ms.body = mkBody(2048)
	md, _ := fetchAndConvertDocument(context.Background(), ms.URL+"/f.ipynb", ms.Client())
	// A truncated read falls back to raw passthrough (md starts with the
	// JSON itself); a full read renders the cell source.
	if !strings.Contains(md, "ZZZ_END") || strings.HasPrefix(md, `{"cells"`) {
		t.Errorf("at-limit body: md is raw passthrough (len %d), want rendered notebook", len(md))
	}

	ms = newMethodServer(t)
	ms.getCT = "application/json"
	// Two bytes over the cap: the cut leaves the JSON invalid, so the
	// notebook renders as raw passthrough text.
	ms.body = mkBody(2048 + 2)
	md, err := fetchAndConvertDocument(context.Background(), ms.URL+"/f.ipynb", ms.Client())
	if err != nil {
		t.Fatalf("over-limit body: error %v", err)
	}
	if !strings.HasPrefix(md, `{"cells"`) {
		t.Errorf("over-limit body: md len %d, want truncated raw passthrough", len(md))
	}
}

func TestWKGetExtBoundaries(t *testing.T) {
	tests := []struct{ path, want string }{
		{".txt", ".txt"},
		{"a.", "."},
		{"dir/file.pdf", ".pdf"},
		{"no/ext", ""},
		{"dir.d/file", ""},
	}
	for _, tt := range tests {
		if got := getExt(tt.path); got != tt.want {
			t.Errorf("getExt(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestWKNewWiresClientAndOptions(t *testing.T) {
	// The provided client must reach the fetch path (not be swapped for the
	// default), observable through a marked transport via Call_.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "marked ok")
	}))
	defer server.Close()

	marked := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		marked = true
		return http.DefaultTransport.RoundTrip(r)
	})}
	tt := New(client)
	res, err := tt.Call(context.Background(), json.RawMessage(fmt.Sprintf(`{"query": %q}`, server.URL)), nil)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out := res.Data.(*Output); out.Mode != "fetch" || out.Content != "marked ok" {
		t.Errorf("output = %+v, want fetch/marked ok", out)
	}
	if !marked {
		t.Error("provided client was not used for the fetch")
	}

	// WithAPIKeys must flow into the built tool: keyed providers show up
	// in the schema's provider description.
	keyed := New(nil, WithAPIKeys(map[string]string{"anysearch": "k"}))
	if s := string(keyed.InputSchema()); !strings.Contains(s, "anysearch") {
		t.Error("schema missing anysearch provider; WithAPIKeys not applied")
	}

	// Search-or-read classification drives collapsed TUI rendering.
	sr, ok := keyed.(tool.ToolWithSearchOrRead)
	if !ok {
		t.Fatal("Web tool must implement ToolWithSearchOrRead")
	}
	if !sr.IsSearchOrRead(nil).IsSearch {
		t.Error("IsSearchOrRead().IsSearch = false, want true")
	}
}

func TestWKDecodeResultIdentifyingFields(t *testing.T) {
	tt := New(nil)
	// Mode set, Content empty: valid legacy wire form.
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(tool.WrapSingleBlock(`{"Mode":"fetch"}`))
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	if o := v.(*Output); o.Mode != "fetch" {
		t.Errorf("mode = %q, want fetch", o.Mode)
	}
	// Content set, Mode empty: still identified, must decode too.
	v, err = tt.(tool.ToolWithDecodeResult).DecodeResult(tool.WrapSingleBlock(`{"Content":"# hi"}`))
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	if o := v.(*Output); o.Content != "# hi" {
		t.Errorf("content = %q, want # hi", o.Content)
	}
	// Neither field set: reject so replay falls back to the wire text.
	if _, err := tt.(tool.ToolWithDecodeResult).DecodeResult(tool.WrapSingleBlock(`{"Raw": null}`)); err == nil || !strings.Contains(err.Error(), "lacks identifying fields") {
		t.Errorf("DecodeResult(no identity) err = %v, want the lacks-identifying-fields rejection", err)
	}
}

func TestWKExecuteFetchHTMLConversion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body><h1>Hi</h1></body></html>")
	}))
	defer server.Close()

	result, err := execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"query": %q}`, server.URL)), &providers.SearchChain{}, server.Client(), nil)
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	output := result.Data.(*Output)
	if output.Content != "# \n\n# Hi" {
		t.Errorf("content = %q, want markdown conversion", output.Content)
	}
}

func TestWKFACDControlCharURL(t *testing.T) {
	// A URL url.Parse rejects must bail with ("", nil) before any request.
	md, err := fetchAndConvertDocument(context.Background(), "http://example.com/%zzfile.pdf", nil)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if md != "" {
		t.Errorf("md = %q, want empty for unparseable URL", md)
	}
}
