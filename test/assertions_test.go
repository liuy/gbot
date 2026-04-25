package assertions_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestWeakAssertions scans all _test.go files in the project for weak assertion patterns
// that violate the 测试断言铁律 (assertion iron law).
//
// Checked patterns:
//
//	P1: _ = xxx.Exec/Scan/Unmarshal/... — discarded setup errors
//	P3: len(x) > 0 — no exact count check
//
// Exemptions:
//   - defer { _ = ... } / t.Cleanup — cleanup is acceptable
//   - Benchmark functions — performance tests
//   - _, _ = xxx.WriteString(...) — write result not needed
func TestWeakAssertions(t *testing.T) {
	projectRoot := findProjectRoot(t)
	if projectRoot == "" {
		t.Fatal("cannot find project root (go.mod)")
	}

	var files []string
	err := filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// Skip vendor, .git, test/ (this package itself)
		rel, _ := filepath.Rel(projectRoot, path)
		for _, skip := range []string{"vendor", ".git", "test"} {
			if strings.HasPrefix(rel, skip+string(filepath.Separator)) || rel == skip {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk project: %v", err)
	}

	t.Logf("Scanning %d test files...", len(files))

	total := 0
	for _, f := range files {
		total += scanFile(t, f, projectRoot)
	}

	if total > 0 {
		t.Logf("\n=== Found %d weak assertion issues ===", total)
		t.Fail()
	} else {
		t.Log("No weak assertion issues found.")
	}
}

// findProjectRoot walks up from CWD to find directory containing go.mod.
func findProjectRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// checkPatterns defines the weak assertion patterns to detect.
type checkPattern struct {
	Name     string
	Regex    *regexp.Regexp
	Level    string // "P1" or "P3"
	TestOnly bool   // if true, only match in _test.go files
	Exempt   func(match string, lines []string, lineIdx int) bool
}

var checkPatterns = []checkPattern{
		{
			Name:     "discarded variable in test (_ = identifier)",
			Regex:    regexp.MustCompile(`^\s*_ = [a-zA-Z_][a-zA-Z0-9_]*\s*(//.*)?$`),
			Level:    "P1",
			TestOnly: true,
			Exempt: func(match string, lines []string, lineIdx int) bool {
				// Exempt: defer cleanup
				if isInDefer(lines, lineIdx) || isInCleanup(lines, lineIdx) {
					return true
				}
				// Exempt: goroutine cleanup
				if isInGoroutine(lines, lineIdx) {
					return true
				}
				return false
			},
		},
	{
		Name:  "discarded setup error (_ = xxx.Method())",
		Regex: regexp.MustCompile(`^\s*_ = .*\.(Scan|Exec|Close|Unmarshal|Marshal|Query|Open|Begin|Commit|Write|Read|Insert|Delete|Update|WriteTokens|ReadTokens|WriteClientConfig|ReadClientConfig|SaveTokens|Release|Wait)\(`),
		Level: "P1",
		Exempt: func(match string, lines []string, lineIdx int) bool {
			// Exempt: defer cleanup
			if isInDefer(lines, lineIdx) || isInCleanup(lines, lineIdx) {
				return true
			}
			// Exempt: _, _ = xxx.WriteString (write result)
			if strings.Contains(match, "WriteString") || strings.Contains(match, "WriteFile") {
				return true
			}
			// Exempt: goroutine cleanup (close in goroutine)
			if isInGoroutine(lines, lineIdx) {
				return true
			}
			// Exempt: HTTP handler closure — unmarshal in test server handlers
			if isInHTTPHandler(lines, lineIdx) {
				return true
			}
			// Exempt: hijack cleanup — conn.Close() after Hijack()
			if isInHijack(lines, lineIdx) {
				return true
			}
			// Exempt: rows.Close() — always safe, idiomatic cleanup
			if strings.Contains(match, "rows.Close(") {
				return true
			}
			// Exempt: Close() errors are standard Go cleanup, not actionable
			if strings.Contains(match, ".Close(") {
				return true
			}
			// Exempt: master/slave.Close() in PTY tests — OS resource cleanup
			if strings.Contains(match, "master.Close(") || strings.Contains(match, "slave.Close(") {
				return true
			}
			return false
		},
	},
	{
		Name:     "len > 0 without exact count",
				TestOnly: true,
		Regex: regexp.MustCompile(`len\([^)]+\)\s*>\s*0`),
		Level: "P3",
		Exempt: func(match string, lines []string, lineIdx int) bool {
			// Exempt: for loop conditions (trim pattern)
			trimmed := strings.TrimSpace(lines[lineIdx])
			if strings.HasPrefix(trimmed, "for ") || strings.HasPrefix(trimmed, "for(") {
				return true
			}
			// Exempt: inside for condition on previous line
			for i := lineIdx - 1; i >= 0 && i >= lineIdx-2; i-- {
				t := strings.TrimSpace(lines[i])
				if strings.HasPrefix(t, "for ") {
					return true
				}
			}
			return false
		},
	},
	{
		Name:  "hardcoded path in file I/O calls (/tmp, /home)",
		Regex: regexp.MustCompile(`(?i)\b(os\.(Open|WriteFile|Create|Mkdir|Remove|Rename|Link)|ioutil\.(ReadFile|WriteFile|ReadDir|MkdirTemp)|os\.OpenFile)\s*\([^)]*"/(tmp|home)[^"]*"`),
		Level: "P3",
		Exempt: func(match string, lines []string, lineIdx int) bool {
			return strings.HasPrefix(strings.TrimSpace(lines[lineIdx]), "//")
		},
	},
	{
		Name:     "t.Skip without message",
				TestOnly: true,
		Regex: regexp.MustCompile(`\bt\.Skip\s*\(\s*[\x27]\s*[\x27]\s*\)`),
		Level: "P3",
		Exempt: func(match string, lines []string, lineIdx int) bool {
			return false
		},
	},
	{
		Name:  "strings.Contains with literal on struct field (use == for exact match)",
		Regex: regexp.MustCompile(`strings\.Contains\(\w+\.\w+,\s*"[^"*%\\]+"\)`),
		Level: "P3",
		Exempt: func(match string, lines []string, lineIdx int) bool {
			trimmed := strings.TrimSpace(lines[lineIdx])
			// Exempt: comments
			if strings.HasPrefix(trimmed, "//") {
				return true
			}
			// Exempt: err.Error() content checks (valid use of Contains)
			if strings.Contains(match, "err.Error()") {
				return true
			}
			// Exempt: negated Contains (!strings.Contains)
			if strings.Contains(lines[lineIdx], "!strings.Contains") {
				return true
			}
			// Exempt: fields that hold complex/multi-line content
			// (Command strings, multi-line output, rendered content)
			for _, field := range []string{".Command", ".Content", ".Output", ".Path", ".Text"} {
				if strings.Contains(match, field) {
					return true
				}
			}
			return false
		},
	},
	{
		Name:     "test result compared only to 0 (use exact value)",
				TestOnly: true,
		Regex: regexp.MustCompile(`\bgot\s*(==|!=|>|>=|<=|<)\s*0\b`),
		Level: "P3",
		Exempt: func(match string, lines []string, lineIdx int) bool {
			// Exempt: comments
			trimmed := strings.TrimSpace(lines[lineIdx])
			if strings.HasPrefix(trimmed, "//") {
				return true
			}
			// Exempt: if there's an exact value check for 'got' nearby in the
			// same test function, this zero-check is a valid early guard.
			// Scan up to 30 lines before and 10 lines after.
			start := max(lineIdx-30, 0)
			for i := start; i <= lineIdx+10 && i < len(lines); i++ {
				if i == lineIdx {
					continue
				}
				line := lines[i]
				// Has exact comparison: got == want, got == <non-zero>
				if (strings.Contains(line, "got ==") || strings.Contains(line, "got !=")) &&
					!strings.Contains(line, "== 0") && !strings.Contains(line, "!= 0") {
					return true
				}
				// Has want calculation: want := ... or want = ...
				if strings.Contains(line, "want :=") || strings.Contains(line, "want=") {
					return true
				}
				// Has assertEqual/assertTokensEqual helper
				if strings.Contains(line, "assertEqual") || strings.Contains(line, "assertTokensEqual") {
					return true
				}
			}
			return false
		},
	},
	{
		Name:  "interface{} should be any (Go 1.18+)",
		Regex: regexp.MustCompile(`interface\{\}`),
		Level: "P3",
		Exempt: func(match string, lines []string, lineIdx int) bool {
			trimmed := strings.TrimSpace(lines[lineIdx])
			// Exempt: comments
			if strings.HasPrefix(trimmed, "//") {
				return true
			}
			// Exempt: string literals (e.g. JSON tags, documentation)
			if strings.HasPrefix(trimmed, "\"") || strings.HasPrefix(trimmed, "`") {
				return true
			}
			return false
		},
	},
		{
			Name:     "error-path test without content verification",
				TestOnly: true,
			Regex: regexp.MustCompile(`(t\.Fatal|t\.Error)\(.*"expected error`),
			Level: "P3",
			Exempt: func(match string, lines []string, lineIdx int) bool {
				for i := lineIdx + 1; i < len(lines) && i <= lineIdx+10; i++ {
					line := lines[i]
					if strings.Contains(line, ".Error()") {
						return true
					}
					if strings.Contains(line, "errors.Is(") || strings.Contains(line, "errors.As(") {
						return true
					}
					if strings.Contains(line, "errMsg") || strings.Contains(line, "errStr") {
						return true
					}
					if strings.Contains(line, "json.Unmarshal") {
						return true
					}
				}
				return false
			},
		},
			{
				Name:  "TODO/FIXME/HACK/XXX comment in production code",
				Regex: regexp.MustCompile(`//\s*(TODO|FIXME|HACK|XXX)\b`),
				Level: "P3",
				Exempt: func(match string, lines []string, lineIdx int) bool {
					// Exempt: inside string literals (test data)
					line := lines[lineIdx]
					idx := strings.Index(line, match)
					if idx > 0 && line[idx-1] == '"' {
						return true
					}
					return false
				},
			},
		{
			Name:  "//nolint suppressions — fix the issue, not hide it",
			Regex: regexp.MustCompile(`//nolint:`),
			Level: "P1",
			Exempt: func(match string, lines []string, lineIdx int) bool {
				trimmed := strings.TrimSpace(lines[lineIdx])
				if strings.HasPrefix(trimmed, "\"") || strings.HasPrefix(trimmed, "`") {
					return true
				}
				return false
			},
		},
}

// scanFile checks a single file for weak assertion patterns.
func scanFile(t *testing.T, path, projectRoot string) int {
	// Parse to find benchmark function ranges
	benchRanges := parseBenchmarkRanges(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read %s: %v", path, err)
		return 0
	}

	lines := strings.Split(string(data), "\n")
	rel, _ := filepath.Rel(projectRoot, path)
	issues := 0

	for _, pat := range checkPatterns {
		if pat.TestOnly && !strings.HasSuffix(path, "_test.go") {
			continue
		}
		for i, line := range lines {
			loc := pat.Regex.FindStringIndex(line)
			if loc == nil {
				continue
			}
			match := line[loc[0]:loc[1]]

			if pat.Exempt != nil && pat.Exempt(match, lines, i) {
				continue
			}

			// Check benchmark exemption via parsed ranges
			if isLineInBenchmark(benchRanges, i+1) {
				continue
			}

			t.Errorf("%s:%d %s: %s", rel, i+1, pat.Level, strings.TrimSpace(match))
			issues++
		}
	}

	return issues
}

// isInDefer checks if the line is inside a defer block.
func isInDefer(lines []string, lineIdx int) bool {
	// Look back for "defer" on same or nearby lines
	for i := lineIdx; i >= 0 && i >= lineIdx-3; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "defer ") {
			return true
		}
		// Stop at function boundary
		if strings.HasPrefix(trimmed, "func ") && !strings.HasPrefix(trimmed, "func()") {
			break
		}
	}
	// Also check: line is inside defer func() { ... }()
	for i := lineIdx; i >= 0 && i >= lineIdx-5; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "defer func()") {
			return true
		}
	}
	return false
}

// isInCleanup checks if the line is inside t.Cleanup(func() { ... }).
func isInCleanup(lines []string, lineIdx int) bool {
	for i := lineIdx; i >= 0 && i >= lineIdx-5; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.Contains(trimmed, "t.Cleanup(") || strings.Contains(trimmed, ".Cleanup(") {
			return true
		}
	}
	return false
}

// isInGoroutine checks if the line is inside a go func() { ... }() block.
func isInGoroutine(lines []string, lineIdx int) bool {
	for i := lineIdx; i >= 0 && i >= lineIdx-10; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "go func()") || strings.HasPrefix(trimmed, "go func(") {
			return true
		}
	}
	return false
}

// parseBenchmarkRanges uses go/parser to find Benchmark* function line ranges.
func parseBenchmarkRanges(path string) [][2]int {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil
	}

	var ranges [][2]int
	for _, decl := range node.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if strings.HasPrefix(fn.Name.Name, "Benchmark") {
			start := fset.Position(fn.Pos()).Line
			end := fset.Position(fn.End()).Line
			ranges = append(ranges, [2]int{start, end})
		}
	}
	return ranges
}

// isLineInBenchmark checks if a line number falls within any benchmark function.
func isLineInBenchmark(ranges [][2]int, line int) bool {
	for _, r := range ranges {
		if line >= r[0] && line <= r[1] {
			return true
		}
	}
	return false
}

// isInHTTPHandler checks if the line is inside an HTTP handler function.
func isInHTTPHandler(lines []string, lineIdx int) bool {
	for i := lineIdx; i >= 0 && i >= lineIdx-15; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.Contains(trimmed, "http.HandlerFunc") ||
			strings.Contains(trimmed, "HandlerFunc(") ||
			strings.Contains(trimmed, "http.HandlerFunc(") {
			return true
		}
	}
	return false
}

// isInHijack checks if the line is after a Hijack() call (cleanup).
func isInHijack(lines []string, lineIdx int) bool {
	for i := lineIdx; i >= 0 && i >= lineIdx-5; i-- {
		if strings.Contains(lines[i], ".Hijack()") {
			return true
		}
	}
	return false
}

// TestMain provides a summary when running standalone.
func TestMain(m *testing.M) {
	fmt.Println("Running weak assertion audit...")
	code := m.Run()
	os.Exit(code)
}

// TestToolRegistrationOrder verifies that all MustRegister calls in cmd/gbot/main.go
// appear BEFORE the engine.New() call. This prevents the "11 tools vs 14 tools" bug
// where tools registered after engine.New() are invisible to AllTools().
func TestToolRegistrationOrder(t *testing.T) {
	projectRoot := findProjectRoot(t)
	if projectRoot == "" {
		t.Fatal("cannot find project root (go.mod)")
	}

	mainPath := filepath.Join(projectRoot, "cmd", "gbot", "main.go")
	src, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("cannot read %s: %v", mainPath, err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, mainPath, src, 0)
	if err != nil {
		t.Fatalf("cannot parse %s: %v", mainPath, err)
	}

	// Walk the AST to find line numbers of:
	//   - reg.MustRegister(...) calls in main() only
	//   - engine.New(...) call in main() only
	type callInfo struct {
		name string
		line int
	}
	var registers []callInfo
	var engineNewLine int

	// Scope to main() function only — ignore createTools() and other helpers.
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					if id.Name == "reg" && sel.Sel.Name == "MustRegister" {
						registers = append(registers, callInfo{
							name: "reg.MustRegister",
							line: fset.Position(call.Lparen).Line,
						})
					}
					if id.Name == "engine" && sel.Sel.Name == "New" {
						engineNewLine = fset.Position(call.Lparen).Line
					}
				}
			}
			return true
		})
		break
	}

	if engineNewLine == 0 {
		t.Fatal("engine.New() not found in main.go")
	}

	for _, r := range registers {
		if r.line > engineNewLine {
			t.Errorf("%s at line %d is AFTER engine.New() at line %d. "+
				"All tools must be registered before engine.New() to ensure "+
				"ToolsProvider captures the full tool set.",
				r.name, r.line, engineNewLine)
		}
	}
}
