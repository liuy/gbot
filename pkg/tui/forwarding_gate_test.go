package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// forwardingExemptions lists message types convertEventToMsg can produce that
// intentionally bypass App.Update's forwarding list. The why lives in the map
// value rather than a bare comment so the gate can assert it is non-empty —
// comments rot silently, an empty map value does not.
// A msg that falls through to Update's default branch kills the one-shot
// readEvents pump, so prove forwarding is genuinely unnecessary before adding
// an entry here.
var forwardingExemptions = map[string]string{}

// TestForwardingGate_HandlerOutputsMustBeForwarded pins the handler→App.Update
// routing contract statically. Unit tests call updateRepl directly, so a case
// that exists but is unreachable through App.Update stays green forever — the
// exact shape of the 2026-11 stuck-streaming incident.
func TestForwardingGate_HandlerOutputsMustBeForwarded(t *testing.T) {
	fset := token.NewFileSet()
	produced := gateProducedMsgs(t, fset, "handler.go")
	forwarded := gateForwardedMsgs(t, fset, "app.go")
	rearmed := gateRearmedMsgs(t, fset, "repl.go")

	names := make([]string, 0, len(produced))
	for name := range produced {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		exempt := forwardingExemptions[name] != ""
		if !forwarded[name] && !exempt {
			t.Errorf("convertEventToMsg can return %s but App.Update does not forward it to updateRepl — "+
				"the readEvents one-shot pump dies on it (stuck-streaming). "+
				"Add it to the forwarding case in app.go or register an exemption with a why.", name)
		}
		// Reachable is not the same as survivable: a handler returning
		// (true, nil) kills the pump just as dead (cf. idleAbortedMsg).
		if !rearmed[name] && !exempt {
			t.Errorf("updateRepl handles %s but never re-arms readEvents — a handler that returns (true, nil) "+
				"kills the pump just as dead as an unforwarded msg (cf. idleAbortedMsg, repl.go:1444). "+
				"Re-arm it or register an exemption with a why.", name)
		}
	}

	for name, why := range forwardingExemptions {
		if !produced[name] {
			t.Errorf("forwardingExemptions entry %q is stale: convertEventToMsg no longer returns it — remove it", name)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("forwardingExemptions entry %q has an empty why — an exemption without a reason is the rot this gate exists to prevent", name)
		}
	}
}

func gateParse(t *testing.T, fset *token.FileSet, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

// gateMethod finds the method named name on receiver type recvType. go/ast has
// no method node: a method is a FuncDecl with a non-nil Recv.
func gateMethod(t *testing.T, file *ast.File, name, recvType string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		if gateTypeName(fn.Recv.List[0].Type) == recvType {
			return fn
		}
	}
	t.Fatalf("gate anchor lost: no method %s with receiver %s — the gate must be updated, not skipped", name, recvType)
	return nil
}

// gateDispatchClauses returns the case clauses of the dispatch type switch that
// is a direct statement of the method body.
func gateDispatchClauses(t *testing.T, fn *ast.FuncDecl) []*ast.CaseClause {
	t.Helper()
	for _, stmt := range fn.Body.List {
		sw, ok := stmt.(*ast.TypeSwitchStmt)
		if !ok {
			continue
		}
		var clauses []*ast.CaseClause
		for _, clause := range sw.Body.List {
			if cc, ok := clause.(*ast.CaseClause); ok {
				clauses = append(clauses, cc)
			}
		}
		if len(clauses) == 0 {
			t.Fatalf("gate anchor lost: type switch in %s has no typed cases", fn.Name.Name)
		}
		return clauses
	}
	t.Fatalf("gate anchor lost: no top-level type switch in %s", fn.Name.Name)
	return nil
}

func gateTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return gateTypeName(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

func gateCalls(expr ast.Node, method string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == method {
			found = true
		}
		return true
	})
	return found
}

// gateProducedMsgs collects the message types convertEventToMsg can return.
// Anything whose static type the gate cannot read is a hard failure: silently
// skipping it is the false green this gate exists to eliminate.
func gateProducedMsgs(t *testing.T, fset *token.FileSet, path string) map[string]bool {
	t.Helper()
	fn := gateMethod(t, gateParse(t, fset, path), "convertEventToMsg", "TUIHandler")
	out := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		switch e := ret.Results[0].(type) {
		case *ast.CompositeLit:
			name := gateTypeName(e.Type)
			if name == "" {
				t.Fatalf("gate cannot statically verify return at %s: %T literal", fset.Position(ret.Pos()), e.Type)
			}
			out[name] = true
		case *ast.Ident:
			if e.Name != "nil" {
				t.Fatalf("gate cannot statically verify return at %s: bare %s — return a message literal or register an exemption with a why",
					fset.Position(ret.Pos()), e.Name)
			}
		default:
			t.Fatalf("gate cannot statically verify return at %s: %T — return a message literal or register an exemption with a why",
				fset.Position(ret.Pos()), e)
		}
		return true
	})
	if len(out) == 0 {
		t.Fatalf("gate anchor lost: convertEventToMsg produced no message types")
	}
	return out
}

// gateForwardedMsgs collects the types App.Update routes to updateRepl.
func gateForwardedMsgs(t *testing.T, fset *token.FileSet, path string) map[string]bool {
	t.Helper()
	fn := gateMethod(t, gateParse(t, fset, path), "Update", "App")
	out := map[string]bool{}
	for _, cc := range gateDispatchClauses(t, fn) {
		if !gateCalls(cc, "updateRepl") {
			continue
		}
		for _, expr := range cc.List {
			name := gateTypeName(expr)
			if name == "" {
				t.Fatalf("gate cannot statically verify case type at %s: %T", fset.Position(cc.Pos()), expr)
			}
			out[name] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("gate anchor lost: App.Update forwards no message type to updateRepl")
	}
	return out
}

// gateRearmedMsgs collects the types whose updateRepl case re-arms readEvents.
// A case listing several types credits all of them, matching gateForwardedMsgs.
func gateRearmedMsgs(t *testing.T, fset *token.FileSet, path string) map[string]bool {
	t.Helper()
	fn := gateMethod(t, gateParse(t, fset, path), "updateRepl", "App")
	out := map[string]bool{}
	for _, cc := range gateDispatchClauses(t, fn) {
		if !gateCalls(cc, "readEvents") {
			continue
		}
		for _, expr := range cc.List {
			name := gateTypeName(expr)
			if name == "" {
				t.Fatalf("gate cannot statically verify case type at %s: %T", fset.Position(cc.Pos()), expr)
			}
			out[name] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("gate anchor lost: updateRepl re-arms readEvents in no case")
	}
	return out
}
