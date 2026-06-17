package assertions_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunAgentParentToolUseID scans all non-test .go files for calls to
// .RunAgent(...) and verifies each call's agenttool.AgentOpts{} composite
// literal explicitly sets the ParentToolUseID field.
//
// Why: forgetting ParentToolUseID silently breaks sub-agent event dispatch.
// NewSubEngine only wires a taggedDispatcher when ParentToolUseID != "",
// so a missing field drops every sub-agent event (text_delta / tool_start /
// tool_end) on the floor. The TUI Agent tool card stays empty for the whole
// run and only renders the final result.
//
// History: this bug shipped twice in production. The check is intentionally
// conservative — only flags the exact pattern (.X.RunAgent(ctx, agenttool.AgentOpts{...}))
// in production code, leaving tests free to omit the field.
func TestRunAgentParentToolUseID(t *testing.T) {
	root := findProjectRoot(t)
	if root == "" {
		t.Fatal("cannot find project root (go.mod)")
	}

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for _, skip := range []string{"vendor", ".git", "test"} {
			if strings.HasPrefix(rel, skip+string(filepath.Separator)) || rel == skip {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanRunAgentCalls(t, path, &offenders)
		return nil
	})
	if err != nil {
		t.Fatalf("walk project: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("Found %d RunAgent call(s) missing ParentToolUseID — this breaks sub-agent event dispatch:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

// scanRunAgentCalls parses one file and inspects every CallExpr whose
// function selector ends in ".RunAgent". For each match it checks the
// agenttool.AgentOpts{...} composite literal passed as the second arg
// and asserts the literal mentions ParentToolUseID.
func scanRunAgentCalls(t *testing.T, path string, offenders *[]string) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Logf("parse %s: %v (skipping)", path, err)
		return
	}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "RunAgent" {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		optsArg, ok := call.Args[1].(*ast.CompositeLit)
		if !ok {
			// Not an inline literal (e.g. a variable passed in). Statically
			// unverifiable — TestCallPassesToolUseID guards the
			// AgentTool.Call path directly.
			return true
		}
		if !mentionsType(optsArg, "AgentOpts") {
			return true
		}
		if hasField(optsArg, "ParentToolUseID") {
			return true
		}
		pos := fset.Position(call.Pos())
		*offenders = append(*offenders,
			pos.String()+": .RunAgent() call missing ParentToolUseID in AgentOpts literal")
		return true
	})
}

func mentionsType(lit *ast.CompositeLit, name string) bool {
	ident, ok := lit.Type.(*ast.Ident)
	if ok {
		return ident.Name == name
	}
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == name
}

func hasField(lit *ast.CompositeLit, name string) bool {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		ident, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		if ident.Name == name {
			return true
		}
	}
	return false
}
