package assertions_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestIntegrationCoverage scans _test.go files and verifies that required
// integration scenarios have non-skipped test functions.
//
// Each RequiredScenario defines a feature area and the integration scenarios
// that MUST have corresponding test functions. A scenario is "covered" when:
//   - A test function matching the NamePattern regex exists in the package
//   - The test function does NOT call t.Skip()
//
// Scenarios are organized by feature area. Add new scenarios when implementing
// new features — this test will fail until integration tests are written.
//
// To add a new required scenario, simply append to requiredScenarios below.
func TestIntegrationCoverage(t *testing.T) {
	projectRoot := findProjectRoot(t)
	if projectRoot == "" {
		t.Fatal("cannot find project root (go.mod)")
	}

	missing := 0
	for _, scenario := range requiredScenarios {
		pkgDir := filepath.Join(projectRoot, scenario.Package)
		if _, err := os.Stat(pkgDir); os.IsNotExist(err) {
			t.Errorf("package directory not found: %s", scenario.Package)
			missing++
			continue
		}

		found, skipped := findTestInPkg(pkgDir, scenario.NamePattern)
		if !found {
			t.Errorf("[MISSING] %s: %s — no test matching /%s/ in %s",
				scenario.Feature, scenario.Description, scenario.NamePattern, scenario.Package)
			missing++
		} else if skipped {
			t.Errorf("[SKIPPED] %s: %s — test exists but calls t.Skip() in %s",
				scenario.Feature, scenario.Description, scenario.Package)
			missing++
		}
	}

	if missing > 0 {
		t.Logf("\n=== %d integration scenarios need tests ===", missing)
		t.Fail()
	} else {
		t.Log("All required integration scenarios are covered.")
	}
}

// RequiredScenario defines an integration test that must exist.
type RequiredScenario struct {
	Feature     string // Feature area (e.g., "auto-compact", "tui/session", "memory/fork")
	Package     string // Package path relative to project root (e.g., "pkg/engine")
	NamePattern string // Regex matching test function name
	Description string // What the test should verify
}

// requiredScenarios lists all integration scenarios that MUST have tests.
// Add entries here when implementing new features.
//
// Naming convention keywords:
//   - Proactive: auto-compact triggered before API call (token threshold)
//   - Reactive: auto-compact triggered after API error (prompt_too_long)
//   - Fork: sub-engine/session fork behavior, isolation from parent
//   - E2E: end-to-end with real components (Store, LLM mock)
//   - Concurrent: thread safety under concurrent access
//   - Integration: test function prefix for cross-component tests
var requiredScenarios = []RequiredScenario{
	// --- auto-compact (pkg/engine) ---
	{
		Feature:     "auto-compact",
		Package:     "pkg/engine",
		NamePattern: `Proactive.*RealCompactor|Proactive.*E2E`,
		Description: "Proactive compact with real AutoCompactor + Store (not mock)",
	},
	{
		Feature:     "auto-compact",
		Package:     "pkg/engine",
		NamePattern: `Reactive.*E2E|Reactive.*RealCompactor`,
		Description: "Reactive compact: API error → real AutoCompactor compact → retry succeeds",
	},
	{
		Feature:     "auto-compact",
		Package:     "pkg/engine",
		NamePattern: `Compact.*Fork.*Isolat|SubEngine.*Compact.*Isolat|Fork.*Compact`,
		Description: "Fork sub-engine compact does NOT affect parent messages",
	},
	{
		Feature:     "auto-compact",
		Package:     "pkg/engine",
		NamePattern: `MultiTurn.*Compact|Compact.*MultiTurn|Compact.*Continue`,
		Description: "Multi-turn conversation grows, triggers compact, then continues correctly",
	},
	{
		Feature:     "auto-compact",
		Package:     "pkg/engine",
		NamePattern: `Compact.*Concurrent|Concurrent.*Compact`,
		Description: "Concurrent access: ProcessNotifications while compact in progress",
	},
	{
		Feature:     "auto-compact",
		Package:     "pkg/engine",
		NamePattern: `Compact.*Session|Compact.*Persist`,
		Description: "After compact, messages are persisted correctly to Store",
	},

	// --- tui/session (pkg/tui) ---
	{
		Feature:     "tui/session",
		Package:     "pkg/tui",
		NamePattern: `Integration.*Fork.*History|Integration.*ForkCarries`,
		Description: "Fork carries conversation history from parent session",
	},
	{
		Feature:     "tui/session",
		Package:     "pkg/tui",
		NamePattern: `Integration.*Fork.*Isolat|Integration.*ForkIsolation`,
		Description: "Fork session messages do NOT leak to parent or siblings",
	},
	{
		Feature:     "tui/session",
		Package:     "pkg/tui",
		NamePattern: `Integration.*Picker.*Session|Integration.*PickerShows`,
		Description: "Picker shows all sessions including forks",
	},
	{
		Feature:     "tui/session",
		Package:     "pkg/tui",
		NamePattern: `Integration.*Switch.*Restore|Integration.*SwitchBack`,
		Description: "Switching back to a session restores its messages",
	},
	{
		Feature:     "tui/session",
		Package:     "pkg/tui",
		NamePattern: `Integration.*NewSession.*Empty|Integration.*NewSessionIs`,
		Description: "New session starts empty with no residual state",
	},

	// --- memory/fork (pkg/memory/short) ---
	{
		Feature:     "memory/fork",
		Package:     "pkg/memory/short",
		NamePattern: `Fork.*Isolat|ForkSession.*Cop`,
		Description: "Fork copies messages and does not share state with parent",
	},
	{
		Feature:     "memory/fork",
		Package:     "pkg/memory/short",
		NamePattern: `Merge.*Back|MergeFork`,
		Description: "MergeForkBack merges child session messages into parent",
	},

	// --- memory/search (pkg/memory/short) ---
	{
		Feature:     "memory/search",
		Package:     "pkg/memory/short",
		NamePattern: `Search.*Message|SearchMessage`,
		Description: "Full-text search across session messages returns correct results",
	},
}

// findTestInPkg searches all _test.go files in a directory for a test function
// matching namePattern. Returns (found, skipped).
func findTestInPkg(dir string, namePattern string) (bool, bool) {
	re := regexp.MustCompile(namePattern)
	fset := token.NewFileSet()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, false
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		filePath := filepath.Join(dir, entry.Name())
		node, err := parser.ParseFile(fset, filePath, nil, 0)
		if err != nil {
			continue
		}

		for _, decl := range node.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			if !re.MatchString(fn.Name.Name) {
				continue
			}

			// Found matching test function. Check if it calls t.Skip().
			hasSkip := hasSkipCall(fn)
			return true, hasSkip
		}
	}

	return false, false
}

// hasSkipCall checks if a function body contains t.Skip() calls.
func hasSkipCall(fn *ast.FuncDecl) bool {
	var found bool
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "Skip" {
			found = true
			return false
		}
		return true
	})
	return found
}
