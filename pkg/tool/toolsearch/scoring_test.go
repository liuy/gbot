package toolsearch

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

// st describes a deferred tool for the ordering tables below.
type st struct {
	name string
	desc string
	hint string
}

func searchOrder(query string, max int, tools ...st) []string {
	deferred := make(map[string]tool.Tool, len(tools))
	for _, x := range tools {
		deferred[x.name] = deferredToolWithHint(x.name, x.desc, x.hint)
	}
	return searchToolsWithKeywords(query, deferred, deferred, max)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The scoring ladder is pinned by ORDER, not by score values: each case is
// built so a single weight change (or a dropped addition) flips the ranking.
func TestSearchOrder_ScoringLadder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		query string
		tools []st
		want  []string
	}{
		{
			name:  "exact > contains > hint > desc",
			query: "zip",
			tools: []st{
				{"ZipTool", "unrelated", ""},
				{"BzipTool", "unrelated", ""},
				{"GammaTool", "unrelated", "zip"},
				{"DeltaTool", "zip related", ""},
			},
			want: []string{"ZipTool", "BzipTool", "GammaTool", "DeltaTool"},
		},
		{
			// contains 5 vs hint 4: either weight moving by one ties the pair,
			// and the tie breaks to the other tool.
			name:  "contains beats hint by one point",
			query: "zip",
			tools: []st{
				{"AgammaTool", "unrelated", "zip"},
				{"ZzipperTool", "unrelated", ""},
			},
			want: []string{"ZzipperTool", "AgammaTool"},
		},
		{
			// hint 4 vs two description hits 2+2: hint-- or desc++ ties it.
			name:  "hint ties double description",
			query: "zip zap",
			tools: []st{
				{"AgammaTool", "unrelated", "zip"},
				{"BdeltaTool", "zip and zap", ""},
			},
			want: []string{"AgammaTool", "BdeltaTool"},
		},
		{
			// MCP exact 12 vs non-MCP exact 10: negating IsMCP ties them and the
			// tie breaks to the alphabetically earlier non-MCP tool.
			name:  "mcp exact beats non-mcp exact",
			query: "zip",
			tools: []st{
				{"mcp__zip__go", "unrelated", ""},
				{"Azip_Zip", "unrelated", ""},
			},
			want: []string{"mcp__zip__go", "Azip_Zip"},
		},
		{
			// 12 vs 5+4+2=11: MCP exact losing one point ties and flips.
			name:  "mcp exact beats contains+hint+desc by one",
			query: "zip",
			tools: []st{
				{"mcp__zip__go", "unrelated", ""},
				{"BzipperTool", "zip", "zip"},
			},
			want: []string{"mcp__zip__go", "BzipperTool"},
		},
		{
			// 5+4+2+2=13 vs 12: MCP exact gaining one point ties and flips to it.
			name:  "multi-term sum beats mcp exact by one",
			query: "zip zap",
			tools: []st{
				{"zzipperTool", "zip and zap", "zip"},
				{"mcp__zip__a", "unrelated", ""},
			},
			want: []string{"zzipperTool", "mcp__zip__a"},
		},
		{
			// MCP contains 6 vs hint+desc 6: MCP contains++ , hint-- or desc--
			// breaks the tie in favour of the MCP tool.
			name:  "hint+desc ties mcp contains",
			query: "zip",
			tools: []st{
				{"BgammaTool", "zip here", "zip"},
				{"mcp__zipx__go", "unrelated", ""},
			},
			want: []string{"BgammaTool", "mcp__zipx__go"},
		},
		{
			// non-MCP contains 5 vs MCP contains 6: either moving by one ties and
			// the tie breaks to the non-MCP tool.
			name:  "mcp contains beats non-mcp contains",
			query: "zip",
			tools: []st{
				{"BzipTool", "unrelated", ""},
				{"mcp__zipx__go", "unrelated", ""},
			},
			want: []string{"mcp__zipx__go", "BzipTool"},
		},
		{
			// exact 10 vs 4+4+2=10, tie breaks to the exact tool: exact-- flips.
			name:  "exact ties three-term sum, name favours exact",
			query: "zip zap",
			tools: []st{
				{"ZipTool", "unrelated", ""},
				{"agammaTool", "zip only", "zip zap"},
			},
			want: []string{"ZipTool", "agammaTool"},
		},
		{
			// same tie, name favours the sum: exact++ flips.
			name:  "exact ties three-term sum, name favours sum",
			query: "zip zap",
			tools: []st{
				{"AgammaTool", "zip only", "zip zap"},
				{"Zip_Zip", "unrelated", ""},
			},
			want: []string{"AgammaTool", "Zip_Zip"},
		},
		{
			// += turned into = keeps only the last term's contribution.
			name:  "desc accumulates across three terms",
			query: "alpha beta gamma",
			tools: []st{
				{"AdescTool", "alpha beta gamma", ""},
				{"BhintTool", "unrelated", "alpha"},
			},
			want: []string{"AdescTool", "BhintTool"},
		},
		{
			name:  "hint accumulates across three terms",
			query: "zip zap zoo",
			tools: []st{
				{"AhintTool", "unrelated", "zip zap zoo"},
				{"ZzipzapTool", "unrelated", ""},
			},
			want: []string{"AhintTool", "ZzipzapTool"},
		},
		{
			name:  "contains accumulates across two terms",
			query: "zip zap",
			tools: []st{
				{"ZzipzapTool", "unrelated", ""},
				{"AhintTool", "unrelated", "zip zap"},
			},
			want: []string{"ZzipzapTool", "AhintTool"},
		},
		{
			name:  "non-mcp exact accumulates across three terms",
			query: "zip zap zoo",
			tools: []st{
				{"Zip_Zap_Zoo", "unrelated", ""},
				{"BhintTool", "unrelated", "zip zap zoo"},
			},
			want: []string{"Zip_Zap_Zoo", "BhintTool"},
		},
		{
			name:  "mcp exact accumulates across three terms",
			query: "zip zap zoo",
			tools: []st{
				{"mcp__zip__zap__zoo", "unrelated", ""},
				{"BhintTool", "unrelated", "zip zap zoo"},
			},
			want: []string{"mcp__zip__zap__zoo", "BhintTool"},
		},
		{
			name:  "mcp contains accumulates across three terms",
			query: "zip zap zoo",
			tools: []st{
				{"mcp__zipx__zapx__zoox", "unrelated", ""},
				{"AhintTool", "unrelated", "zip zap zoo"},
			},
			want: []string{"mcp__zipx__zapx__zoox", "AhintTool"},
		},
		{
			// part-exact must win over an earlier part-contains in the same name.
			name:  "part exact outranks part contains in same name",
			query: "zip",
			tools: []st{
				{"Bzip_Zip", "unrelated", ""},
				{"AgammaTool", "unrelated", "zip"},
			},
			want: []string{"Bzip_Zip", "AgammaTool"},
		},
		{
			// The exact part is the second one: an early exit from the part loop
			// would leave only the contains match (5) and lose this tie.
			name:  "exact part found after a contains part",
			query: "zip zap",
			tools: []st{
				{"Abzip_Zip", "unrelated", ""},
				{"ZgammaTool", "zip only", "zip zap"},
			},
			want: []string{"Abzip_Zip", "ZgammaTool"},
		},
		{
			// Same tie as above, mirrored names: score starting at -1 flips it.
			name:  "double description beats hint by name order",
			query: "zip zap",
			tools: []st{
				{"AdeltaTool", "zip and zap", ""},
				{"BgammaTool", "unrelated", "zip"},
			},
			want: []string{"AdeltaTool", "BgammaTool"},
		},
		{
			// A one-character required term is still a required term.
			name:  "single char required term",
			query: "+a",
			tools: []st{
				{"A_Tool", "unrelated", ""},
				{"EpsTool", "nothing here", ""},
			},
			want: []string{"A_Tool"},
		},
		{
			// One required plus two optional terms.
			name:  "one required two optional",
			query: "+alpha beta gamma",
			tools: []st{
				{"Alpha_Tool", "beta and gamma", ""},
				{"EpsTool", "nothing here", ""},
			},
			want: []string{"Alpha_Tool"},
		},
		{
			// Every required term must be checked, not just the first.
			name:  "two required terms both enforced",
			query: "+alpha +beta",
			tools: []st{
				{"Alpha_Tool", "unrelated", ""},
				{"AlphaBeta_Tool", "unrelated", ""},
				{"Beta_Tool", "unrelated", ""},
			},
			want: []string{"AlphaBeta_Tool"},
		},
		{
			// A non-empty hint that does not match must not count as a match:
			// treating "hint != \"\"" as the match would score GammaTool +4.
			name:  "non-matching hint scores nothing",
			query: "zip",
			tools: []st{
				{"GammaTool", "unrelated", "totally unrelated hint"},
				{"DeltaTool", "zip related", ""},
			},
			want: []string{"DeltaTool"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := searchOrder(c.query, 10, c.tools...)
			if !equalStrings(got, c.want) {
				t.Errorf("searchOrder(%q) = %v, want %v", c.query, got, c.want)
			}
		})
	}
}

// The required-term pre-filter must accept every match channel and reject a
// tool that matches the required term through none of them.
func TestSearchOrder_RequiredTermChannels(t *testing.T) {
	t.Parallel()
	got := searchOrder("+alpha", 10,
		st{"Alpha_Tool", "unrelated", ""},  // part exact
		st{"XalphaxTool", "unrelated", ""}, // part contains
		st{"DeltaTool", "unrelated", "alpha stuff"},
		st{"GammaTool", "does alpha things", ""},
		st{"EpsTool", "nothing here", ""},
	)
	want := []string{"Alpha_Tool", "XalphaxTool", "DeltaTool", "GammaTool"}
	if !equalStrings(got, want) {
		t.Errorf("required-channel result = %v, want %v", got, want)
	}
}

// A required term must gate candidates even when the optional term scores the
// tool highly, and the optional term must still contribute to scoring.
func TestSearchOrder_RequiredGatesOptionalScoring(t *testing.T) {
	t.Parallel()
	got := searchOrder("+alpha beta", 10,
		st{"Alpha_Zap", "beta related", ""},            // 10 + 2
		st{"Alpha_Tool", "unrelated", ""},              // 10
		st{"Beta_Tool", "unrelated", "unrelated hint"}, // 10 but fails the required term
		st{"EpsTool", "nothing here", ""},
	)
	want := []string{"Alpha_Zap", "Alpha_Tool"}
	if !equalStrings(got, want) {
		t.Errorf("required+optional result = %v, want %v", got, want)
	}
}

// Optional terms are partitioned away from required ones: with no "+" in the
// query every term is optional and each still scores.
func TestSearchOrder_AllTermsOptional(t *testing.T) {
	t.Parallel()
	got := searchOrder("alpha beta", 10,
		st{"Alpha_Tool", "unrelated", ""},
		st{"Beta_Tool", "unrelated", ""},
		st{"EpsTool", "nothing here", ""},
	)
	want := []string{"Alpha_Tool", "Beta_Tool"}
	if !equalStrings(got, want) {
		t.Errorf("all-optional result = %v, want %v", got, want)
	}
}

// A lone "+" is an optional term, never an empty required term: an empty
// required term would match every part via strings.Contains(part, "").
func TestSearchOrder_LonePlusMatchesNothing(t *testing.T) {
	t.Parallel()
	got := searchOrder("+", 10,
		st{"Alpha_Tool", "unrelated", ""},
		st{"Beta_Tool", "unrelated", ""},
	)
	if len(got) != 0 {
		t.Errorf("query %q matched %v, want no matches", "+", got)
	}
}

func TestSearchOrder_MCPPrefixPath(t *testing.T) {
	t.Parallel()
	// "mcp__" alone is too short for the prefix path and no keyword matches it.
	only := []st{{"mcp__x__y", "unrelated", ""}}
	if got := searchOrder("mcp__", 10, only...); len(got) != 0 {
		t.Errorf("query %q matched %v, want no matches", "mcp__", got)
	}
	// One char past the prefix marker routes through the prefix path.
	if got := searchOrder("mcp__x", 10, only...); !equalStrings(got, []string{"mcp__x__y"}) {
		t.Errorf("query %q matched %v, want [mcp__x__y]", "mcp__x", got)
	}
	// A prefix with no name hits must fall through to keyword scoring, not
	// return the empty prefix-match slice.
	fallback := []st{{"SomeTool", "the mcp__zzz tool", ""}}
	if got := searchOrder("mcp__zzz", 10, fallback...); !equalStrings(got, []string{"SomeTool"}) {
		t.Errorf("query %q matched %v, want [SomeTool] via keyword fallback", "mcp__zzz", got)
	}
	// Prefix matches are sorted, not returned in map order.
	got := searchOrder("mcp__server", 10,
		st{"mcp__server__c", "unrelated", ""},
		st{"mcp__server__a", "unrelated", ""},
		st{"mcp__server__b", "unrelated", ""},
	)
	want := []string{"mcp__server__a", "mcp__server__b", "mcp__server__c"}
	if !equalStrings(got, want) {
		t.Errorf("prefix matches = %v, want %v", got, want)
	}
}

// Which subset survives the maxResults break depends on map order, so the
// deterministic part is the count and the sortedness.
func TestSearchOrder_MCPPrefixLimit(t *testing.T) {
	t.Parallel()
	got := searchOrder("mcp__server", 2,
		st{"mcp__server__a", "unrelated", ""},
		st{"mcp__server__b", "unrelated", ""},
		st{"mcp__server__c", "unrelated", ""},
	)
	if len(got) != 2 {
		t.Fatalf("prefix matches with max 2 = %v, want exactly 2", got)
	}
	for _, name := range got {
		if !strings.HasPrefix(name, "mcp__server__") {
			t.Errorf("prefix match %q outside the queried prefix", name)
		}
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("prefix matches %v are not sorted", got)
	}
}

func TestSearchOrder_MaxResultsExact(t *testing.T) {
	t.Parallel()
	got := searchOrder("searchable", 2,
		st{"ToolA", "searchable alpha", ""},
		st{"ToolB", "searchable beta", ""},
		st{"ToolC", "searchable gamma", ""},
	)
	want := []string{"ToolA", "ToolB"}
	if !equalStrings(got, want) {
		t.Errorf("max 2 result = %v, want %v", got, want)
	}
}

func TestSearchOrder_ZeroScoreExcluded(t *testing.T) {
	t.Parallel()
	got := searchOrder("zzzzzxyz", 5,
		st{"FileRead", "Read file contents", ""},
		st{"FileEdit", "Edit file contents", ""},
	)
	if len(got) != 0 {
		t.Errorf("non-matching query matched %v, want none", got)
	}
}

// ---------------------------------------------------------------------------
// Execute gaps
// ---------------------------------------------------------------------------

func TestExecute_DefaultMaxResultsIsFive(t *testing.T) {
	t.Parallel()
	tools := toolMap(
		deferredTool("T1", "searchable one"),
		deferredTool("T2", "searchable two"),
		deferredTool("T3", "searchable three"),
		deferredTool("T4", "searchable four"),
		deferredTool("T5", "searchable five"),
		deferredTool("T6", "searchable six"),
	)
	result, err := Execute(context.Background(), json.RawMessage(`{"query":"searchable"}`), makeTctx(tools))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 5 {
		t.Errorf("default max_results returned %d matches (%v), want 5", len(out.Matches), out.Matches)
	}
}

func TestExecute_MaxResultsOneHonoured(t *testing.T) {
	t.Parallel()
	tools := toolMap(
		deferredTool("T1", "searchable one"),
		deferredTool("T2", "searchable two"),
		deferredTool("T3", "searchable three"),
	)
	result, err := Execute(context.Background(), json.RawMessage(`{"query":"searchable","max_results":1}`), makeTctx(tools))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 1 {
		t.Fatalf("max_results=1 returned %d matches (%v), want 1", len(out.Matches), out.Matches)
	}
	if out.Query != "searchable" {
		t.Errorf("Query = %q, want searchable", out.Query)
	}
	if out.TotalDeferredTools != 3 {
		t.Errorf("TotalDeferredTools = %d, want 3", out.TotalDeferredTools)
	}
	if out.PendingMCPServers != nil {
		t.Errorf("PendingMCPServers = %v, want nil when matches were found", out.PendingMCPServers)
	}
}

// The tool wrapper must dispatch to Execute, not to a BuildTool fallback.
func TestNew_CallDispatchesToExecute(t *testing.T) {
	t.Parallel()
	tools := toolMap(deferredTool("FileRead", "Read file contents"))
	result, err := New().Call(context.Background(), json.RawMessage(`{"query":"Read"}`), makeTctx(tools))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("Call data = %T, want *Output", result.Data)
	}
	if len(out.Matches) != 1 || out.Matches[0] != "FileRead" {
		t.Errorf("Call matches = %v, want [FileRead]", out.Matches)
	}
}

func TestExecute_SelectTrimsSpaces(t *testing.T) {
	t.Parallel()
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
		deferredTool("FileEdit", "Edit file contents"),
	)
	result, err := Execute(context.Background(), json.RawMessage(`{"query":"select: FileRead , FileEdit "}`), makeTctx(tools))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 2 {
		t.Fatalf("select with spaces matched %v, want both tools", out.Matches)
	}
	if out.Matches[0] != "FileRead" || out.Matches[1] != "FileEdit" {
		t.Errorf("select order = %v, want [FileRead FileEdit]", out.Matches)
	}
}

func TestExecute_SelectNotFoundShape(t *testing.T) {
	t.Parallel()
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
		regularTool("Grep", "Search in files"),
	)
	result, err := Execute(context.Background(), json.RawMessage(`{"query":"select:Nope"}`), makeTctx(tools))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := result.Data.(*Output)
	if out.Matches == nil || len(out.Matches) != 0 {
		t.Errorf("Matches = %#v, want an empty non-nil slice", out.Matches)
	}
	if out.Query != "select:Nope" {
		t.Errorf("Query = %q, want select:Nope", out.Query)
	}
	if out.TotalDeferredTools != 1 {
		t.Errorf("TotalDeferredTools = %d, want 1", out.TotalDeferredTools)
	}
}

func TestExecute_KeywordNoMatchShape(t *testing.T) {
	t.Parallel()
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
		deferredTool("FileEdit", "Edit file contents"),
	)
	result, err := Execute(context.Background(), json.RawMessage(`{"query":"zzzzzxyz"}`), makeTctx(tools))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Matches) != 0 {
		t.Errorf("Matches = %v, want empty", out.Matches)
	}
	if out.Query != "zzzzzxyz" {
		t.Errorf("Query = %q, want zzzzzxyz", out.Query)
	}
	if out.TotalDeferredTools != 2 {
		t.Errorf("TotalDeferredTools = %d, want 2", out.TotalDeferredTools)
	}
}

func TestExecute_OnePendingServerReported(t *testing.T) {
	t.Parallel()
	tools := toolMap(deferredTool("FileRead", "Read file contents"))
	result, err := Execute(context.Background(), json.RawMessage(`{"query":"zzzzzxyz"}`),
		makeTctxWithPending(tools, []string{"slack"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.PendingMCPServers) != 1 || out.PendingMCPServers[0] != "slack" {
		t.Errorf("PendingMCPServers = %v, want [slack]", out.PendingMCPServers)
	}
}

func TestExecute_CodeOnlyDeferredExcluded(t *testing.T) {
	t.Parallel()
	tools := toolMap(
		deferredTool("FileRead", "Read file contents"),
		codeOnlyDeferredTool("Internal", "searchable internal tool"),
		regularTool("Grep", "searchable grep tool"),
	)
	result, err := Execute(context.Background(), json.RawMessage(`{"query":"searchable"}`), makeTctx(tools))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := result.Data.(*Output)
	if out.TotalDeferredTools != 1 {
		t.Errorf("TotalDeferredTools = %d, want 1 (code-only deferred tool must not count)", out.TotalDeferredTools)
	}
	if len(out.Matches) != 0 {
		t.Errorf("code-only tool matched: %v, want no matches", out.Matches)
	}
}

func TestExecute_ParseErrorWrapsSyntaxError(t *testing.T) {
	t.Parallel()
	_, err := Execute(context.Background(), json.RawMessage(`{query`), makeTctx(toolMap()))
	if err == nil {
		t.Fatal("Execute must reject malformed JSON")
	}
	if err.Error() != `parse input: invalid character 'q' looking for beginning of object key string` {
		t.Errorf("error = %q, want %q", err.Error(), `parse input: invalid character 'q' looking for beginning of object key string`)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("error %v must wrap *json.SyntaxError", err)
	}
}

// errorDescTool fails Description, which toolDescription must swallow as "".
type errorDescTool struct {
	*mockTool
}

func (e *errorDescTool) Description(json.RawMessage) (string, error) {
	// The description text is searchable on purpose: swallowing the error as
	// "" is what keeps this tool out of keyword results.
	return "searchable description", errors.New("description unavailable")
}

func TestToolDescriptionErrorBecomesEmpty(t *testing.T) {
	t.Parallel()
	name := "BrokenTool"
	bad := &errorDescTool{mockTool: deferredTool(name, "searchable description")}
	deferred := map[string]tool.Tool{name: bad}
	got := searchToolsWithKeywords("searchable", deferred, deferred, 5)
	if len(got) != 0 {
		t.Errorf("tool with failing Description matched %v, want no matches", got)
	}
}

func TestDecodeResult_IdentifyingFields(t *testing.T) {
	t.Parallel()
	tl := New()
	dec, ok := tl.(tool.ToolWithDecodeResult)
	if !ok {
		t.Fatal("ToolSearch must implement ToolWithDecodeResult")
	}
	// Query alone identifies the result.
	v, err := dec.DecodeResult(tool.WrapSingleBlock(`{"query":"zip","matches":[]}`))
	if err != nil {
		t.Fatalf("query-only wire rejected: %v", err)
	}
	if o := v.(*Output); o.Query != "zip" || len(o.Matches) != 0 {
		t.Errorf("decoded = %+v, want query=zip matches=[]", o)
	}
	// Matches alone identify the result.
	v, err = dec.DecodeResult(tool.WrapSingleBlock(`{"query":"","matches":["FileRead"]}`))
	if err != nil {
		t.Fatalf("matches-only wire rejected: %v", err)
	}
	if o := v.(*Output); len(o.Matches) != 1 || o.Matches[0] != "FileRead" {
		t.Errorf("decoded = %+v, want matches=[FileRead]", o)
	}
	// Neither: rejected with the pinned message.
	_, err = dec.DecodeResult(tool.WrapSingleBlock(`{"name":"gbot"}`))
	if err == nil {
		t.Fatal("all-zero wire must be rejected")
	}
	want := "toolsearch: decoded output lacks identifying fields (not a legacy JSON result)"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestDecodeResult_BlockExtractorError(t *testing.T) {
	t.Parallel()
	tl := New()
	dec := tl.(tool.ToolWithDecodeResult)
	_, err := dec.DecodeResult(json.RawMessage(`{"query":"zip"}`))
	if err == nil {
		t.Fatal("non-array wire must be rejected")
	}
	want := `tool: UnmarshalSingleBlock expects array-form content, got "{\"query\":\"zip\"}"`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestNew_ToolDefFields(t *testing.T) {
	t.Parallel()
	tl := New()
	if got := tool.SearchHint(tl); got != "" {
		t.Errorf("SearchHint() = %q, want empty", got)
	}
	if got := tl.MaxResultSize(); got != 100000 {
		t.Errorf("MaxResultSize() = %d, want 100000", got)
	}
	if got := tool.IsDeferred(tl); got != false {
		t.Errorf("IsDeferred() = %v, want false", got)
	}
	if got := tl.IsReadOnly(nil); got != true {
		t.Errorf("IsReadOnly() = %v, want true", got)
	}
	// Invalid input still yields the full prompt, not the empty query.
	desc, err := tl.Description(json.RawMessage(`not json`))
	if err != nil {
		t.Fatalf("Description(invalid): %v", err)
	}
	if desc != toolPrompt {
		t.Errorf("Description(invalid) = %q, want the full prompt", desc)
	}
}
