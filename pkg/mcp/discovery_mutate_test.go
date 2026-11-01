package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Relay fixture: an in-process MCP server whose selected RPCs never answer.
//
// Swallowing a request (instead of closing the session) is what makes the
// timeout/cancellation and semaphore paths reachable: a closed session fails
// instantly and never blocks, so the ctx.Done() branch of the discovery
// semaphore and the %w cause chain of a hung RPC would stay unobservable.
// ---------------------------------------------------------------------------

type mutateRelay struct {
	session *mcp.ClientSession

	mu      sync.Mutex
	seen    map[string]int
	dropped map[string]bool

	// firstDrop fires the moment a droppable request reaches the relay, i.e.
	// the moment the calling goroutine is parked inside the RPC.
	firstDrop chan struct{}
	once      sync.Once
}

func (r *mutateRelay) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[method]
}

func newMutateRelay(t *testing.T, drop ...string) *mutateRelay {
	t.Helper()

	clientToRelayR, clientToRelayW := io.Pipe()
	relayToServerR, relayToServerW := io.Pipe()
	serverToClientR, serverToClientW := io.Pipe()

	r := &mutateRelay{
		seen:      map[string]int{},
		dropped:   map[string]bool{},
		firstDrop: make(chan struct{}),
	}
	for _, m := range drop {
		r.dropped[m] = true
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "relay-server", Version: "v1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "relay_tool", Description: "relay tool"}, noopToolHandler)
	srvConn := make(chan error, 1)
	go func() {
		ss, err := srv.Connect(context.Background(), &mcp.IOTransport{
			Reader: relayToServerR,
			Writer: serverToClientW,
		}, nil)
		if err == nil {
			err = ss.Wait()
		}
		srvConn <- err
	}()

	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		sc := bufio.NewScanner(clientToRelayR)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<22)
		for sc.Scan() {
			line := sc.Text()
			var hdr struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal([]byte(line), &hdr); err != nil {
				continue
			}
			r.mu.Lock()
			r.seen[hdr.Method]++
			dropNow := r.dropped[hdr.Method]
			r.mu.Unlock()
			if dropNow {
				r.once.Do(func() { close(r.firstDrop) })
				continue
			}
			if _, werr := io.WriteString(relayToServerW, line+"\n"); werr != nil {
				return
			}
		}
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "relay-client", Version: "v1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.IOTransport{
		Reader: serverToClientR,
		Writer: clientToRelayW,
	}, nil)
	if err != nil {
		t.Fatalf("relay client connect: %v", err)
	}
	r.session = session

	t.Cleanup(func() {
		_ = session.Close()
		_ = clientToRelayW.Close()
		_ = relayToServerW.Close()
		_ = serverToClientW.Close()
		<-relayDone
		if werr := <-srvConn; werr != nil && !errors.Is(werr, io.EOF) {
			t.Errorf("relay server session: %v", werr)
		}
	})
	return r
}

// mutateStdioConfig / mutateToolsCap build the discovery inputs repeatedly.
func mutateStdioScoped() ScopedMcpServerConfig {
	return ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser}
}

func mutateToolsCap() *mcp.ServerCapabilities {
	return &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}
}

// ---------------------------------------------------------------------------
// Batch-size env parsing: the n > 0 guard is the whole point.
// ---------------------------------------------------------------------------

func TestGetLocalBatchSize_NonPositiveAndBoundary(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want int
	}{
		{"0", 3},  // zero must fall back: cap-0 semaphore deadlocks BatchDiscovery
		{"-1", 3}, // negative must fall back
		{"1", 1},  // smallest accepted value must survive
		{"not-int", 3},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("MCP_SERVER_CONNECTION_BATCH_SIZE", tc.env)
			if got := GetLocalBatchSize(); got != tc.want {
				t.Errorf("GetLocalBatchSize() with %q = %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}

func TestGetRemoteBatchSize_NonPositiveAndBoundary(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want int
	}{
		{"0", 20},
		{"-1", 20},
		{"1", 1},
		{"not-int", 20},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("MCP_REMOTE_SERVER_CONNECTION_BATCH_SIZE", tc.env)
			if got := GetRemoteBatchSize(); got != tc.want {
				t.Errorf("GetRemoteBatchSize() with %q = %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}

func TestFetchCacheCapacity_MatchesTSConstant(t *testing.T) {
	if fetchCacheCapacity != 20 {
		t.Errorf("fetchCacheCapacity = %d, want 20 (client.ts:1726 MCP_FETCH_CACHE_SIZE)", fetchCacheCapacity)
	}
}

// ---------------------------------------------------------------------------
// IsLocalServer — each disjunct of the transport test is load-bearing.
// ---------------------------------------------------------------------------

// mutateEmptyTransportConfig models a config whose transport is unset ("").
// IsLocalServer treats "" as stdio for backwards compatibility (types.ts:30).
type mutateEmptyTransportConfig struct{}

func (mutateEmptyTransportConfig) GetTransport() Transport { return "" }

func TestIsLocalServer_EachTransportDisjunct(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  McpServerConfig
		want bool
	}{
		{"stdio", &StdioConfig{Command: "x"}, true},
		{"sdk", &SDKConfig{Name: "x"}, true},
		{"empty-transport", mutateEmptyTransportConfig{}, true},
		{"sse", &SSEConfig{URL: "http://x"}, false},
		{"sse-ide", &SSEIDEConfig{URL: "http://x"}, false},
		{"ws-ide", &WSIDEConfig{URL: "ws://x"}, false},
		{"http", &HTTPConfig{URL: "http://x"}, false},
		{"ws", &WSConfig{URL: "ws://x"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLocalServer(ScopedMcpServerConfig{Config: tc.cfg}); got != tc.want {
				t.Errorf("IsLocalServer(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Capability guards — the nil-Capabilities and nil-Tools disjuncts.
// ---------------------------------------------------------------------------

func TestFetchToolsForServer_ToolsCapabilityNilSkipsList(t *testing.T) {
	conn, cleanup := connectTestServer(t, &mcp.Tool{Name: "listed_tool"})
	defer cleanup()
	conn.Capabilities = &mcp.ServerCapabilities{Tools: nil}

	cache := mustLRU[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tools == nil || len(tools) != 0 {
		t.Errorf("tools = %v, want non-nil empty when Tools capability is absent (TS: [])", tools)
	}
	if _, ok := cache.Get(conn.Name); ok {
		t.Error("capability-less server must not populate the cache")
	}
}

func TestFetchCommandsForServer_PromptsCapabilityNilSkipsList(t *testing.T) {
	conn, cleanup := connectTestServer(t, &mcp.Tool{Name: "listed_tool"})
	defer cleanup()
	conn.Capabilities = &mcp.ServerCapabilities{Prompts: nil}

	cache := mustLRU[string, []MCPCommand](10)
	commands, err := FetchCommandsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if commands == nil || len(commands) != 0 {
		t.Errorf("commands = %v, want non-nil empty when Prompts capability is absent (TS: [])", commands)
	}
}

func TestFetchCommandsForServer_NilSessionWithPromptsCapability(t *testing.T) {
	conn := &ConnectedServer{
		Name:         "no-session",
		Session:      nil,
		Config:       mutateStdioScoped(),
		Capabilities: &mcp.ServerCapabilities{Prompts: &mcp.PromptCapabilities{}},
	}
	cache := mustLRU[string, []MCPCommand](10)
	commands, err := FetchCommandsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if commands == nil || len(commands) != 0 {
		t.Errorf("commands = %v, want non-nil empty for nil session (TS: [])", commands)
	}
}

// ---------------------------------------------------------------------------
// Error wrapping: %w must keep the cause reachable through errors.Is.
// ---------------------------------------------------------------------------

func TestFetchToolsForServer_ListToolsErrorWrapsCause(t *testing.T) {
	relay := newMutateRelay(t, "tools/list")
	conn := &ConnectedServer{
		Name:         "hang-server",
		Session:      relay.session,
		Config:       mutateStdioScoped(),
		Capabilities: mutateToolsCap(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cache := mustLRU[string, []DiscoveredTool](10)
	_, err := FetchToolsForServer(ctx, conn, cache)
	if err == nil {
		t.Fatal("want error from hung tools/list")
	}
	if !strings.Contains(err.Error(), `tools/list`) {
		t.Errorf("error = %v, want it to name tools/list", err)
	}
	if !strings.Contains(err.Error(), `"hang-server"`) {
		t.Errorf("error = %v, want it to name the server", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

func TestFetchResourcesForServer_ListResourcesErrorWrapsCause(t *testing.T) {
	relay := newMutateRelay(t, "resources/list")
	conn := &ConnectedServer{
		Name:         "hang-res",
		Session:      relay.session,
		Config:       mutateStdioScoped(),
		Capabilities: &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cache := mustLRU[string, []ServerResource](10)
	_, err := FetchResourcesForServer(ctx, conn, cache)
	if err == nil {
		t.Fatal("want error from hung resources/list")
	}
	if !strings.Contains(err.Error(), `resources/list`) {
		t.Errorf("error = %v, want it to name resources/list", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

func TestFetchCommandsForServer_ListPromptsErrorWrapsCause(t *testing.T) {
	relay := newMutateRelay(t, "prompts/list")
	conn := &ConnectedServer{
		Name:         "hang-prompts",
		Session:      relay.session,
		Config:       mutateStdioScoped(),
		Capabilities: &mcp.ServerCapabilities{Prompts: &mcp.PromptCapabilities{}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cache := mustLRU[string, []MCPCommand](10)
	_, err := FetchCommandsForServer(ctx, conn, cache)
	if err == nil {
		t.Fatal("want error from hung prompts/list")
	}
	if !strings.Contains(err.Error(), `prompts/list`) {
		t.Errorf("error = %v, want it to name prompts/list", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

// ---------------------------------------------------------------------------
// Description truncation boundary: exactly MaxMCPDescriptionLength stays whole.
// ---------------------------------------------------------------------------

func TestFetchToolsForServer_DescriptionTruncationBoundary(t *testing.T) {
	exact := strings.Repeat("e", MaxMCPDescriptionLength)
	over := strings.Repeat("e", MaxMCPDescriptionLength+1)

	conn, cleanup := connectTestServer(t,
		&mcp.Tool{Name: "exact_desc", Description: exact},
		&mcp.Tool{Name: "over_desc", Description: over},
	)
	defer cleanup()

	cache := mustLRU[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(tools))
	}
	byName := map[string]string{}
	for _, tl := range tools {
		byName[tl.OriginalName] = tl.Description
	}
	if byName["exact_desc"] != exact {
		t.Errorf("description of length %d was truncated; want it unchanged", MaxMCPDescriptionLength)
	}
	wantOver := over[:MaxMCPDescriptionLength] + "… [truncated]"
	if byName["over_desc"] != wantOver {
		t.Errorf("over-long description = %q, want %q", byName["over_desc"], wantOver)
	}
}

// ---------------------------------------------------------------------------
// Annotation defaults: a present annotations object with nil hints is false.
// ---------------------------------------------------------------------------

func TestFetchToolsForServer_NilAnnotationHintsDefaultFalse(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{Name: "hintless", Description: "d", Annotations: &mcp.ToolAnnotations{}},
	)
	defer cleanup()

	cache := mustLRU[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	got := tools[0].Annotations
	want := ToolAnnotations{ReadOnlyHint: false, DestructiveHint: false, OpenWorldHint: false}
	if got != want {
		t.Errorf("Annotations = %+v, want %+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// _meta extraction: searchHint whitespace collapse and alwaysLoad.
// ---------------------------------------------------------------------------

func TestFetchToolsForServer_MetaSearchHintAndAlwaysLoad(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{
			Name:        "meta_tool",
			Description: "d",
			Meta: mcp.Meta{
				"anthropic/searchHint": "find\n  files\tfast",
				"anthropic/alwaysLoad": true,
			},
		},
	)
	defer cleanup()

	cache := mustLRU[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if tools[0].SearchHint != "find files fast" {
		t.Errorf("SearchHint = %q, want %q", tools[0].SearchHint, "find files fast")
	}
	if !tools[0].AlwaysLoad {
		t.Error("AlwaysLoad = false, want true for _meta anthropic/alwaysLoad true")
	}
}
func TestFetchToolsForServer_MetaAlwaysLoadFalseStaysFalse(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{
			Name:        "optout_tool",
			Description: "d",
			Meta: mcp.Meta{
				"anthropic/alwaysLoad": false,
			},
		},
	)
	defer cleanup()
	cache := mustLRU[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if tools[0].AlwaysLoad {
		t.Error("AlwaysLoad = true, want false for explicit _meta anthropic/alwaysLoad false (TS: === true)")
	}
}

func TestFetchToolsForServer_MetaWrongTypesLeaveDefaults(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{
			Name:        "wrongtype_tool",
			Description: "d",
			Meta: mcp.Meta{
				"anthropic/searchHint": 42,
				"anthropic/alwaysLoad": "yes",
			},
		},
	)
	defer cleanup()

	cache := mustLRU[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if tools[0].SearchHint != "" {
		t.Errorf("SearchHint = %q, want empty for non-string meta", tools[0].SearchHint)
	}
	if tools[0].AlwaysLoad {
		t.Error("AlwaysLoad = true, want false for non-bool meta")
	}
}

// ---------------------------------------------------------------------------
// Input schema sanitization must run on the marshaled schema.
// ---------------------------------------------------------------------------

func TestFetchToolsForServer_InputSchemaSanitized(t *testing.T) {
	conn, cleanup := connectTestServer(t,
		&mcp.Tool{
			Name:        "zwsp_tool",
			Description: "d",
			InputSchema: map[string]any{"type": "object", "title": "a\u200bb"},
		},
	)
	defer cleanup()

	cache := mustLRU[string, []DiscoveredTool](10)
	tools, err := FetchToolsForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	if strings.Contains(string(tools[0].InputSchema), "\u200b") {
		t.Errorf("InputSchema = %s, want zero-width space removed", tools[0].InputSchema)
	}
	want := `{"title":"ab","type":"object"}`
	if string(tools[0].InputSchema) != want {
		t.Errorf("InputSchema = %s, want %s", tools[0].InputSchema, want)
	}
}

// ---------------------------------------------------------------------------
// Resource field mapping — every field of ServerResource is asserted.
// ---------------------------------------------------------------------------

func TestFetchResourcesForServer_MapsAllFields(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	server.AddResource(&mcp.Resource{
		URI:         "test://doc",
		Name:        "doc",
		Description: "the doc",
		MIMEType:    "text/markdown",
	}, noopResourceHandler)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	conn := &ConnectedServer{
		Name:         "res-server",
		Session:      session,
		Config:       mutateStdioScoped(),
		Capabilities: &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}},
	}
	cache := mustLRU[string, []ServerResource](10)
	resources, err := FetchResourcesForServer(context.Background(), conn, cache)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(resources))
	}
	want := ServerResource{
		URI:         "test://doc",
		Name:        "doc",
		Description: "the doc",
		MimeType:    "text/markdown",
		Server:      "res-server",
	}
	if resources[0] != want {
		t.Errorf("resource = %+v, want %+v", resources[0], want)
	}
}

// ---------------------------------------------------------------------------
// BatchDiscovery — remote group, semaphore release, cancellation.
// ---------------------------------------------------------------------------

func TestBatchDiscovery_RemoteServerIsDiscovered(t *testing.T) {
	conn, cleanup := connectTestServer(t, &mcp.Tool{Name: "remote_tool", Description: "d"})
	defer cleanup()
	conn.Name = "remote-1"
	conn.Config = ScopedMcpServerConfig{Config: &HTTPConfig{Type: TransportHTTP, URL: "http://example.com"}, Scope: ScopeUser}

	results := BatchDiscovery(context.Background(), []*ConnectedServer{conn},
		mustLRU[string, []DiscoveredTool](10),
		mustLRU[string, []ServerResource](10),
		mustLRU[string, []MCPCommand](10))

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].ServerName != "remote-1" {
		t.Errorf("ServerName = %q, want %q", results[0].ServerName, "remote-1")
	}
	if results[0].Error != nil {
		t.Fatalf("Error = %v, want nil", results[0].Error)
	}
	if len(results[0].Tools) != 1 || results[0].Tools[0].OriginalName != "remote_tool" {
		t.Errorf("Tools = %+v, want one remote_tool", results[0].Tools)
	}
}

func TestBatchDiscovery_SemaphoreSerializesAndCancelUsesCtxErr(t *testing.T) {
	t.Setenv("MCP_SERVER_CONNECTION_BATCH_SIZE", "1")

	relay := newMutateRelay(t, "tools/list")

	const n = 5
	conns := make([]*ConnectedServer, n)
	for i := range conns {
		conns[i] = &ConnectedServer{
			Name:         "sem-" + string(rune('a'+i)),
			Session:      relay.session,
			Config:       mutateStdioScoped(),
			Capabilities: mutateToolsCap(),
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan []ServerDiscovery, 1)
	go func() {
		done <- BatchDiscovery(ctx, conns,
			mustLRU[string, []DiscoveredTool](10),
			mustLRU[string, []ServerResource](10),
			mustLRU[string, []MCPCommand](10))
	}()

	// The relay sees a tools/list only when a goroutine is parked inside the
	// RPC, i.e. holding the only semaphore slot.
	select {
	case <-relay.firstDrop:
	case <-time.After(10 * time.Second): // REAL-TIME: fail fast if no RPC ever parks
		t.Fatal("no tools/list request reached the relay")
	}
	if got := relay.count("tools/list"); got != 1 {
		t.Errorf("tools/list requests in flight = %d, want 1 (semaphore of size 1 must serialize)", got)
	}
	cancel()

	var results []ServerDiscovery
	select {
	case results = <-done:
	case <-time.After(30 * time.Second): // REAL-TIME: bounded wait for BatchDiscovery unwind
		t.Fatal("BatchDiscovery did not return after context cancel")
	}

	if len(results) != n {
		t.Fatalf("results = %d, want %d", len(results), n)
	}

	// Exactly one goroutine holds the slot, so at least n-1 must report the
	// bare ctx error (not a wrapped tools/list error) — that is the
	// semaphore's ctx.Done() branch, and it must carry the server name.
	cancelled := 0
	for i, r := range results {
		if r.ServerName != conns[i].Name {
			t.Errorf("results[%d].ServerName = %q, want %q", i, r.ServerName, conns[i].Name)
		}
		if r.Error == context.Canceled {
			cancelled++
			continue
		}
		if r.Error == nil {
			t.Errorf("results[%d].Error = nil, want an error after cancel", i)
		} else if !strings.Contains(r.Error.Error(), "tools/list") {
			t.Errorf("results[%d].Error = %v, want a tools/list failure or context.Canceled", i, r.Error)
		}
	}
	if cancelled < n-1 {
		t.Errorf("results reporting context.Canceled = %d, want at least %d", cancelled, n-1)
	}
}

// ---------------------------------------------------------------------------
// discoverForServer — a resource failure must short-circuit and stick.
// ---------------------------------------------------------------------------

func TestDiscoverForServer_ResourceErrorShortCircuitsAndKeepsError(t *testing.T) {
	relay := newMutateRelay(t, "resources/list")
	conn := &ConnectedServer{
		Name:    "partial-server",
		Session: relay.session,
		Config:  mutateStdioScoped(),
		Capabilities: &mcp.ServerCapabilities{
			Tools:     &mcp.ToolCapabilities{},
			Resources: &mcp.ResourceCapabilities{},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	d := discoverForServer(ctx, conn,
		mustLRU[string, []DiscoveredTool](10),
		mustLRU[string, []ServerResource](10),
		mustLRU[string, []MCPCommand](10))

	if d.ServerName != "partial-server" {
		t.Errorf("ServerName = %q, want %q", d.ServerName, "partial-server")
	}
	if d.Error == nil {
		t.Fatal("Error = nil, want the resources/list failure to survive")
	}
	if !errors.Is(d.Error, context.DeadlineExceeded) {
		t.Errorf("Error = %v, want it to wrap context.DeadlineExceeded", d.Error)
	}
	if len(d.Tools) != 1 || d.Tools[0].OriginalName != "relay_tool" {
		t.Errorf("Tools = %+v, want the successfully fetched relay_tool", d.Tools)
	}
	if d.Resources != nil {
		t.Errorf("Resources = %v, want nil after the failure", d.Resources)
	}
	if d.Commands != nil {
		t.Errorf("Commands = %v, want nil: discovery must stop after a resource error", d.Commands)
	}
}

// ---------------------------------------------------------------------------
// Collapse allowlists — every listed name must classify.
// ---------------------------------------------------------------------------

func TestClassifyMcpToolForCollapse_AllSearchNames(t *testing.T) {
	names := []string{
		"search", "search_files", "search_code",
		"grep", "grep_file", "search_file_content",
		"find", "find_files", "find_in_files",
		"glob", "list_directory", "list_files",
		"query", "query_index", "search_symbols",
		"web_search", "web_fetch",
	}
	if len(names) != 17 {
		t.Fatalf("search allowlist has %d entries, want 17", len(names))
	}
	for _, name := range names {
		got := ClassifyMcpToolForCollapse("srv", name)
		if got != (CollapseClassification{IsSearch: true, IsRead: false}) {
			t.Errorf("ClassifyMcpToolForCollapse(%q) = %+v, want {IsSearch:true}", name, got)
		}
	}
}

func TestClassifyMcpToolForCollapse_AllReadNames(t *testing.T) {
	names := []string{
		"read", "read_file", "read_files",
		"get", "get_file", "get_content",
		"fetch", "fetch_file", "fetch_content",
		"view", "view_file", "cat",
		"head", "tail", "show",
		"read_resource", "get_resource",
		"inspect", "dump", "peek",
		"type", "display", "print",
		"open", "open_file", "load",
		"retrieve", "examine", "browse",
	}
	if len(names) != 29 {
		t.Fatalf("read allowlist has %d entries, want 29", len(names))
	}
	for _, name := range names {
		got := ClassifyMcpToolForCollapse("srv", name)
		if got != (CollapseClassification{IsSearch: false, IsRead: true}) {
			t.Errorf("ClassifyMcpToolForCollapse(%q) = %+v, want {IsRead:true}", name, got)
		}
	}
}

func TestClassifyMcpToolForCollapse_NamesOutsideAllowlists(t *testing.T) {
	for _, name := range []string{"write", "delete", "execute", "search_files_extra", "reads"} {
		got := ClassifyMcpToolForCollapse("srv", name)
		if got != (CollapseClassification{}) {
			t.Errorf("ClassifyMcpToolForCollapse(%q) = %+v, want {false,false}", name, got)
		}
	}
}

// ---------------------------------------------------------------------------
// toSnakeCase — the A-Z range bounds and the range itself.
// ---------------------------------------------------------------------------

func TestToSnakeCase_NonLetterAndRangeBounds(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"abc", "abc"}, // lowercase must not enter the upper branch
		{"a!b", "a!b"}, // punctuation below 'A' must not enter it
		{"a1b", "a1b"}, // digits below 'A' must not enter it
		{"Abc", "abc"}, // 'A' is the lower bound
		{"Zoo", "zoo"}, // 'Z' is the upper bound
		{"aZ", "a_z"},  // 'Z' mid-string gets the separator
		{"searchZ", "search_z"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := toSnakeCase(tc.in); got != tc.want {
				t.Errorf("toSnakeCase(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
