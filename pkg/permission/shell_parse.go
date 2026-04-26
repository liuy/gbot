package permission

import (
	"fmt"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// MaxSubCommands limits the number of extracted sub-commands.
// Aligned with TS splitCommand_DEPRECATED max of 50.
const MaxSubCommands = 50

// Shell interpreters that support -c argument.
// Source: bashPermissions.ts:196-226 — BARE_SHELL_PREFIXES (shell subset)
var shellInterpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true,
	"csh": true, "tcsh": true, "ksh": true, "dash": true,
}

// Safe wrapper commands to strip before matching.
// Source: bashPermissions.ts:532-560 — SAFE_WRAPPER_PATTERNS
var safeWrapperPatterns = []*regexp.Regexp{
	// timeout [flags] <duration> <command>
	regexp.MustCompile(`^timeout[\t ]+(?:(?:--(?:foreground|preserve-status|verbose)|(?:--(?:kill-after|signal)=[\w.+\-]+|(?:--(?:kill-after|signal)[\t ]+[\w.+\-]+|-[ks][\t ]+[\w.+\-]+|-[ks][\w.+\-]+|-v)))[\t ]+)*(?:--[\t ]+)?\d+(?:\.\d+)?[smhd]?[\t ]+`),
	// time [--] <command>
	regexp.MustCompile(`^time[\t ]+(?:--[\t ]+)?`),
	// nice [-n N] [-N] <command>
	regexp.MustCompile(`^nice(?:[\t ]+-n[\t ]+-?\d+|[\t ]+-\d+)?[\t ]+(?:--[\t ]+)?`),
	// stdbuf -o0 -eL <command>
	regexp.MustCompile(`^stdbuf(?:[\t ]+-[ioe][LN0-9]+)+[\t ]+(?:--[\t ]+)?`),
	// nohup [--] <command>
	regexp.MustCompile(`^nohup[\t ]+(?:--[\t ]+)?`),
}

// safeEnvVarPattern matches env var assignments with safe values only.
// Source: bashPermissions.ts:575 — ENV_VAR_PATTERN in stripSafeWrappers
// Only matches unquoted values with safe characters (no $(), `, $var, ;|&).
var safeEnvVarPattern = regexp.MustCompile(`^([A-Za-z_]\w*)=([A-Za-z0-9_./:\-]+)[\t ]+`)

// allEnvVarPattern matches all env var assignments for deny/ask stripping.
// Source: bashPermissions.ts:759-760 — broader pattern for stripAllLeadingEnvVars
// Note: uses string concatenation because backtick can't appear in Go raw string literals.
var allEnvVarPattern = regexp.MustCompile(
	`^([A-Za-z_]\w*(?:\[[^\]]*\])?)\+?=(?:'[^'\n\r]*'|"(?:\\.|[^"$` + "`" + `\\\n\r])*"|\\.|[^ \t\n\r$` + "`" + `;|&()<>'"])*[\t ]+`)

// bashParser is a package-level parser instance safe for concurrent use.
// Avoids allocating a new parser on every call.
var bashParser = syntax.NewParser(syntax.KeepComments(false), syntax.Variant(syntax.LangBash))

// bashPrinter is a package-level printer instance safe for concurrent use.
var bashPrinter = syntax.NewPrinter()

// ParseShellCommand uses mvdan/sh AST to extract all executable commands.
//
// Source: AST-based extraction replacing manual string splitting.
// Uses syntax.Variant(syntax.LangBash) for full bash support.
//
// Returns list of actual command strings. Parse failure → error → deny (fail-secure).
// Max 50 sub-commands (aligned with TS).
func ParseShellCommand(command string) ([]string, error) {
	if command == "" {
		return nil, nil
	}

	parser := bashParser
	prog, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		// Parse failure → error → deny (fail-secure)
		return nil, fmt.Errorf("shell parse error: %w", err)
	}

	var commands []string
	walkAST(prog, &commands)

	if len(commands) > MaxSubCommands {
		commands = commands[:MaxSubCommands]
	}
	return commands, nil
}

// walkAST recursively walks the AST to extract all CallExpr command strings.
// Handles BinaryCmd (&&/||/;|), Subshell, CmdSubst, and shell interpreter recursion.
func walkAST(node syntax.Node, commands *[]string) {
	if len(*commands) >= MaxSubCommands {
		return
	}

	switch n := node.(type) {
	case *syntax.File:
		walkStmts(n.Stmts, commands)
	case *syntax.Stmt:
		if n.Cmd != nil {
			walkAST(n.Cmd, commands)
		}
	case *syntax.BinaryCmd:
		walkAST(n.X, commands)
		walkAST(n.Y, commands)
	case *syntax.Subshell:
		walkStmts(n.Stmts, commands)
	case *syntax.CmdSubst:
		// $() — recurse into inner commands
		walkStmts(n.Stmts, commands)
	case *syntax.CallExpr:
		if len(n.Args) == 0 {
			return
		}
		// Walk Word parts to find embedded CmdSubst (e.g. echo $(rm -rf /))
		for _, arg := range n.Args {
			for _, part := range arg.Parts {
				if cs, ok := part.(*syntax.CmdSubst); ok {
					walkStmts(cs.Stmts, commands)
				}
			}
		}
		cmd := reconstructCommand(n)
		if cmd == "" {
			return
		}

		// Shell interpreter recursion:
		// If first word is a shell interpreter and has -c argument, parse inner command.
		firstWord := wordLiteral(n.Args[0])
		if shellInterpreters[firstWord] && hasCFlag(n.Args) {
			innerCmd := getInnerCommand(n.Args)
			if innerCmd != "" {
				innerCmds, err := ParseShellCommand(innerCmd)
				if err == nil {
					*commands = append(*commands, innerCmds...)
					return
				}
				// If inner parse fails, use the inner command as-is
				*commands = append(*commands, innerCmd)
				return
			}
		}

		*commands = append(*commands, cmd)
	case *syntax.IfClause:
		walkStmts(n.Cond, commands)
		walkStmts(n.Then, commands)
		if n.Else != nil {
			walkAST(n.Else, commands)
		}
	case *syntax.WhileClause:
		walkStmts(n.Cond, commands)
		walkStmts(n.Do, commands)
	case *syntax.ForClause:
		walkStmts(n.Do, commands)
	case *syntax.CaseClause:
		for _, ci := range n.Items {
			walkStmts(ci.Stmts, commands)
		}
	case *syntax.FuncDecl:
		if n.Body != nil {
			walkAST(n.Body, commands)
		}
	}
}

// walkStmts walks a slice of statements.
func walkStmts(stmts []*syntax.Stmt, commands *[]string) {
	for _, s := range stmts {
		if len(*commands) >= MaxSubCommands {
			return
		}
		walkAST(s, commands)
	}
}

// reconstructCommand rebuilds a command string from a CallExpr using syntax.Printer.
// Uses Printer not Word.Lit() which returns empty for expansions.
func reconstructCommand(call *syntax.CallExpr) string {
	// Build a Stmt containing just this CallExpr for printing
	var b strings.Builder
	stmt := &syntax.Stmt{Cmd: call}
	if err := bashPrinter.Print(&b, stmt); err != nil {
		// Fallback: join word literals
		parts := make([]string, 0, len(call.Args))
		for _, w := range call.Args {
			parts = append(parts, wordLiteral(w))
		}
		return strings.Join(parts, " ")
	}
	result := strings.TrimSpace(b.String())
	// Strip trailing newlines
	result = strings.TrimRight(result, "\n")
	return result
}

// wordLiteral extracts the literal string from a Word.
// Returns empty string for expansions/variables.
func wordLiteral(w *syntax.Word) string {
	if len(w.Parts) != 1 {
		// Complex word with expansions — use printer
		return printWord(w)
	}
	switch p := w.Parts[0].(type) {
	case *syntax.Lit:
		return p.Value
	default:
		// Non-literal (expansion, etc) — use printer
		return printWord(w)
	}
}

// printWord uses syntax.Printer to render a Word as a string.
func printWord(w *syntax.Word) string {
	var b strings.Builder
	_ = bashPrinter.Print(&b, &syntax.Stmt{Cmd: &syntax.CallExpr{Args: []*syntax.Word{w}}})
	s := strings.TrimSpace(b.String())
	return strings.TrimRight(s, "\n")
}

// hasCFlag checks if the call args contain a -c flag.
func hasCFlag(args []*syntax.Word) bool {
	for _, w := range args[1:] {
		lit := wordLiteral(w)
		if lit == "-c" {
			return true
		}
	}
	return false
}

// getInnerCommand extracts the command string from the -c argument.
// Strips surrounding quotes since the parser preserves them.
func getInnerCommand(args []*syntax.Word) string {
	for i, w := range args[1:] {
		if wordLiteral(w) == "-c" && i+2 <= len(args)-1 {
			inner := wordLiteral(args[i+2])
			// Strip surrounding quotes if present
			if len(inner) >= 2 {
				if (inner[0] == '"' && inner[len(inner)-1] == '"') ||
					(inner[0] == '\'' && inner[len(inner)-1] == '\'') {
					inner = inner[1 : len(inner)-1]
				}
			}
			return inner
		}
	}
	return ""
}

// needsShellMatching checks if a command contains shell metacharacters
// that warrant AST parsing. Fast-path guard.
func needsShellMatching(cmd string) bool {
	return strings.ContainsAny(cmd, "&|;`\n") ||
		strings.Contains(cmd, "$(")
}

// StripSafeWrappers strips safe wrapper commands and env vars from a command.
//
// Source: bashPermissions.ts:524-614 — stripSafeWrappers
// Two-phase: first strip safe env vars, then strip wrapper commands.
// SECURITY: uses [\t ]+ not \s+ to avoid matching across newlines.
func StripSafeWrappers(command string) string {
	stripped := command
	prev := ""

	// Phase 1: Strip leading safe env vars and comments
	for stripped != prev {
		prev = stripped
		stripped = stripCommentLines(stripped)
		if m := safeEnvVarPattern.FindStringSubmatch(stripped); m != nil {
			if safeEnvVars[m[1]] {
				stripped = safeEnvVarPattern.ReplaceAllString(stripped, "")
			}
		}
	}

	// Phase 2: Strip wrapper commands (timeout, time, nice, nohup, stdbuf)
	prev = ""
	for stripped != prev {
		prev = stripped
		stripped = stripCommentLines(stripped)
		for _, pat := range safeWrapperPatterns {
			stripped = pat.ReplaceAllString(stripped, "")
		}
	}

	return strings.TrimSpace(stripped)
}

// StripAllLeadingEnvVars strips ALL leading env var prefixes from a command.
// Used for deny/ask rules to prevent bypass via FOO=bar denied_command.
//
// Source: bashPermissions.ts:733-776 — stripAllLeadingEnvVars
func StripAllLeadingEnvVars(command string) string {
	stripped := command
	prev := ""

	for stripped != prev {
		prev = stripped
		stripped = stripCommentLines(stripped)
		stripped = allEnvVarPattern.ReplaceAllString(stripped, "")
	}

	return strings.TrimSpace(stripped)
}

// stripCommentLines strips lines starting with #.
// Source: bashPermissions.ts:500-522
// Returns original command if all lines are comments/empty (aligned with TS).
func stripCommentLines(cmd string) string {
	lines := strings.Split(cmd, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "#") {
			result = append(result, line)
		}
	}
	if len(result) == 0 {
		return cmd
	}
	return strings.Join(result, "\n")
}

// safeEnvVars is the allowlist of safe env var names.
// Source: bashPermissions.ts:378-430 — SAFE_ENV_VARS
var safeEnvVars = map[string]bool{
	// Go
	"GOEXPERIMENT": true, "GOOS": true, "GOARCH": true,
	"CGO_ENABLED": true, "GO111MODULE": true,
	// Rust
	"RUST_BACKTRACE": true, "RUST_LOG": true,
	// Node
	"NODE_ENV": true,
	// Python
	"PYTHONUNBUFFERED": true, "PYTHONDONTWRITEBYTECODE": true,
	// Pytest
	"PYTEST_DISABLE_PLUGIN_AUTOLOAD": true, "PYTEST_DEBUG": true,
	// Locale
	"LANG": true, "LANGUAGE": true, "LC_ALL": true,
	"LC_CTYPE": true, "LC_TIME": true, "CHARSET": true,
	// Terminal
	"TERM": true, "COLORTERM": true, "NO_COLOR": true,
	"FORCE_COLOR": true, "TZ": true,
	// Colors
	"LS_COLORS": true, "LSCOLORS": true,
	"GREP_COLOR": true, "GREP_COLORS": true, "GCC_COLORS": true,
	// Display
	"TIME_STYLE": true, "BLOCK_SIZE": true, "BLOCKSIZE": true,
}

