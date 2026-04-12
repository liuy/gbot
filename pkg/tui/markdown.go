package tui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	ast "github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/parser"
	"github.com/liuy/gbot/pkg/tool"
)

// Render converts markdown text to ANSI-styled terminal output.
// Source: utils/markdown.ts applyMarkdown → Go port
func Render(text string) string {
	extensions := parser.CommonExtensions | parser.AutoHeadingIDs | parser.Footnotes
	// Disable strikethrough: ~ is often used for "approximate" (e.g., ~100),
	// not actual strikethrough. Matches TS: marked.use({ tokenizer: { del() { return undefined } } })
	extensions &^= parser.Strikethrough

	p := parser.NewWithExtensions(extensions)

	doc := p.Parse([]byte(text))

	var buf strings.Builder
	r := &ansiRenderer{
		w:         &buf,
		listStack: []listCtx{},
	}

	ast.WalkFunc(doc, func(node ast.Node, entering bool) ast.WalkStatus {
		return r.renderNode(node, entering)
	})

	// TS: applyMarkdown does .join('').trim()
	return strings.TrimSpace(buf.String())
}

// RenderWidth renders markdown with word wrapping.
func RenderWidth(text string, width int) string {
	result := Render(text)
	if width <= 0 {
		return result
	}
	return wordWrap(result, width)
}

type listCtx struct {
	ordered bool
	counter int
}

// tableCollector buffers table cell data during AST walk for proper column-width rendering.
type tableCollector struct {
	header   []string
	rows     [][]string
	align    []ast.CellAlignFlags
	current  []string // cells being collected for current row
	inHeader bool
}

// ansiRenderer walks the markdown AST and writes ANSI-styled output.
type ansiRenderer struct {
	w           io.Writer
	listStack   []listCtx
	table       *tableCollector // non-nil when inside a table
	savedWriter []io.Writer     // stack for writer-swap during inline styling / blockquote
}

// ---- ANSI SGR codes for inline styling (matches TS chalk output) ----
// Using targeted reset codes (22=boldOff, 23=italicOff, 24=underlineOff)
// ensures nested styles compose correctly.
const (
	ansiBoldOn      = "\x1b[1m"
	ansiBoldOff     = "\x1b[22m"
	ansiItalicOn    = "\x1b[3m"
	ansiItalicOff   = "\x1b[23m"
	ansiULOn        = "\x1b[4m"
	ansiULOff       = "\x1b[24m"
	ansiReset       = "\x1b[0m"
	ansiFgWhite     = "\x1b[38;5;15m"
	ansiBgPurple    = "\x1b[48;5;62m"
	ansiFgBlue      = "\x1b[38;5;12m"
	ansiFgGray      = "\x1b[38;5;243m"
	ansiFgDimGray = "\x1b[38;5;246m"
)

func (r *ansiRenderer) write(s string) {
	// When collecting table cells, route content into the current cell
	// instead of the main output buffer. This ensures inline nodes
	// (Code, Emph, Strong, etc.) render correctly inside table cells.
	if r.table != nil && len(r.table.current) > 0 {
		r.table.current[len(r.table.current)-1] += s
		return
	}
	_, _ = r.w.Write([]byte(s))
}

// needsBlockSeparator returns true if node should emit a blank-line separator
// before its next sibling. Applies to block-level children of Document and
// BlockQuote (matching TS "space" token behavior).
func needsBlockSeparator(node ast.Node) bool {
	parent := node.GetParent()
	if parent == nil {
		return false
	}
	switch parent.(type) {
	case *ast.Document, *ast.BlockQuote:
	default:
		return false
	}
	children := parent.GetChildren()
	for i, child := range children {
		if child == node {
			return i < len(children)-1
		}
	}
	return false
}

func (r *ansiRenderer) pushList(ordered bool) {
	r.listStack = append(r.listStack, listCtx{ordered: ordered, counter: 0})
}

func (r *ansiRenderer) popList() {
	if len(r.listStack) > 0 {
		r.listStack = r.listStack[:len(r.listStack)-1]
	}
}

func (r *ansiRenderer) currentList() *listCtx {
	return &r.listStack[len(r.listStack)-1]
}

func (r *ansiRenderer) listDepth() int {
	return len(r.listStack)
}

// pushStyleBuffer saves the current writer and swaps to a new buffer.
// Children content will be collected into the buffer. Call popStyleBuffer
// to restore the original writer and apply the style to the buffered content.
// This mirrors TS: chalk.bold(children.map(formatToken).join(''))
func (r *ansiRenderer) pushStyleBuffer() {
	r.savedWriter = append(r.savedWriter, r.w)
	var buf strings.Builder
	r.w = &buf
}

// popStyleBufferANSI restores the previous writer and writes the buffered content
// wrapped in raw ANSI start/end sequences. Uses targeted reset codes (e.g. \x1b[22m
// for bold off) instead of universal reset (\x1b[0m) so nested styles compose correctly.
func (r *ansiRenderer) popStyleBufferANSI(startSeq, endSeq string) {
	buf, ok := r.w.(*strings.Builder)
	var content string
	if ok {
		content = buf.String()
	}
	// Restore previous writer
	if len(r.savedWriter) > 0 {
		r.w = r.savedWriter[len(r.savedWriter)-1]
		r.savedWriter = r.savedWriter[:len(r.savedWriter)-1]
	}
	if content != "" {
		r.write(startSeq + content + endSeq)
	}
}

func (r *ansiRenderer) renderNode(node ast.Node, entering bool) ast.WalkStatus {
	switch n := node.(type) {

	// ---- Block nodes ----

	case *ast.Document:
		return ast.GoToNext

	case *ast.Heading:
		if entering {
			r.pushStyleBuffer()
		} else {
			if n.Level == 1 {
				// bold + italic + underline, targeted resets
				r.popStyleBufferANSI(ansiBoldOn+ansiItalicOn+ansiULOn, ansiBoldOff+ansiItalicOff+ansiULOff)
			} else {
				// bold only
				r.popStyleBufferANSI(ansiBoldOn, ansiBoldOff)
			}
			r.write("\n\n")
		}

	case *ast.Paragraph:
		if !entering {
			r.write("\n")
			if needsBlockSeparator(node) {
				r.write("\n")
			}
		}

	case *ast.BlockQuote:
		if entering {
			// Swap writer to buffer — render inner content first, then post-process
			// like TS: split by \n, prefix each non-empty line with "│ " + italic.
			r.savedWriter = append(r.savedWriter, r.w)
			var buf strings.Builder
			r.w = &buf
		} else {
			// Collect rendered inner content
			var inner string
			if buf, ok := r.w.(*strings.Builder); ok {
				inner = buf.String()
			}
			// Restore previous writer
			if len(r.savedWriter) > 0 {
				r.w = r.savedWriter[len(r.savedWriter)-1]
				r.savedWriter = r.savedWriter[:len(r.savedWriter)-1]
			}
			// Post-process: split by \n, add │ prefix + italic to each non-empty line.
			bar := ansiFgGray + "│ " + ansiReset
			lines := strings.Split(inner, "\n")
			var out strings.Builder
			for i, line := range lines {
				plain := strings.TrimSpace(stripANSI(line))
				if plain != "" {
					out.WriteString(bar + ansiItalicOn + line + ansiItalicOff)
				} else if i == len(lines)-1 {
					// Skip the very last trailing empty from Paragraph's \n
					continue
				}
				// Empty non-last lines: keep as blank line (no content, \n added below)
				if i < len(lines)-1 {
					out.WriteString("\n")
				}
			}
			r.write(out.String())
				if needsBlockSeparator(node) {
					r.write("\n")
				}
			}

	case *ast.List:
		if entering {
			ordered := n.ListFlags&ast.ListTypeOrdered != 0
			r.pushList(ordered)
		} else {
			r.popList()
			if needsBlockSeparator(node) {
				r.write("\n")
			}
		}

	case *ast.ListItem:
		if entering {
			lc := r.currentList()
			if lc != nil {
				lc.counter++
				depth := r.listDepth()
				indent := strings.Repeat("  ", depth-1)
				if lc.ordered {
					num := getListNumber(depth, lc.counter)
					r.write(indent + num + ". ")
				} else {
					r.write(indent + "- ")
				}
			}
		}

	case *ast.CodeBlock:
		if entering {
			r.write(highlightCode(string(n.Literal), string(n.Info)))
			r.write("\n")
			if needsBlockSeparator(node) {
				r.write("\n")
			}
		}
		return ast.SkipChildren

	case *ast.HorizontalRule:
		if entering {
			r.write(ansiFgGray + "───" + ansiReset)
			if needsBlockSeparator(node) {
				r.write("\n")
			}
		}

	// ---- Table ----

	case *ast.Table:
		if entering {
			r.table = &tableCollector{}
		} else {
			r.renderTable()
			r.table = nil
			r.write("\n")
			if needsBlockSeparator(node) {
				r.write("\n")
			}
		}

	case *ast.TableHeader:
		if r.table != nil {
			r.table.inHeader = entering
		}

	case *ast.TableBody:
		return ast.GoToNext

	case *ast.TableRow:
		if r.table != nil && entering {
			r.table.current = nil
		} else if r.table != nil && !entering {
			if r.table.current != nil {
				if r.table.inHeader {
					r.table.header = r.table.current
				} else {
					r.table.rows = append(r.table.rows, r.table.current)
				}
			}
			r.table.current = nil
		}

	case *ast.TableCell:
		if entering && r.table != nil {
			r.table.current = append(r.table.current, "")
			// Collect alignment from header cells
			if r.table.inHeader {
				r.table.align = append(r.table.align, n.Align)
			}
		}

	// ---- Inline nodes ----

	case *ast.Text:
		if entering {
			content := string(n.Literal)
			// Don't linkify inside tables (would interfere with width calculation)
			if r.table == nil {
				content = linkifyIssueReferences(content)
			}
			r.write(content)
		}

	case *ast.Softbreak:
		r.write("\n")

	case *ast.Hardbreak:
		r.write("\n")
	case *ast.Code:
		if entering {
			r.write(ansiFgWhite + ansiBgPurple + string(n.Literal) + ansiReset)
		}

	case *ast.Emph:
		if entering {
			r.pushStyleBuffer()
		} else {
			r.popStyleBufferANSI(ansiItalicOn, ansiItalicOff)
		}

	case *ast.Strong:
		if entering {
			r.pushStyleBuffer()
		} else {
			r.popStyleBufferANSI(ansiBoldOn, ansiBoldOff)
		}

	case *ast.Link:
		if entering {
			// mailto: links show email as plain text
			if strings.HasPrefix(string(n.Destination), "mailto:") {
				email := strings.TrimPrefix(string(n.Destination), "mailto:")
				r.write(email)
				return ast.SkipChildren
			}
			r.pushStyleBuffer()
		} else {
			// mailto already handled in entering phase
			if strings.HasPrefix(string(n.Destination), "mailto:") {
				return ast.GoToNext
			}
			// Collect buffered child text
			var linkText string
			if buf, ok := r.w.(*strings.Builder); ok {
				linkText = buf.String()
			}
			// Restore previous writer
			if len(r.savedWriter) > 0 {
				r.w = r.savedWriter[len(r.savedWriter)-1]
				r.savedWriter = r.savedWriter[:len(r.savedWriter)-1]
			}
			if len(n.Destination) > 0 {
				dest := string(n.Destination)
				if supportsHyperlinks() {
					plainText := stripANSI(linkText)
					if plainText != "" && plainText != dest {
						r.write(createHyperlink(dest, linkText))
					} else {
						r.write(createHyperlink(dest))
					}
				} else {
					// Style the link text + show URL in parens
					r.write(ansiFgBlue + ansiULOn + linkText + ansiULOff + ansiReset)
					r.write(ansiFgDimGray + fmt.Sprintf(" (%s)", dest) + ansiReset)
				}
			}
		}

	case *ast.Image:
		if entering {
			r.write(ansiFgBlue + string(n.Destination) + ansiReset)
		}
		return ast.SkipChildren

	case *ast.HTMLBlock:
		return ast.SkipChildren

	case *ast.HTMLSpan:
		return ast.SkipChildren

	case *ast.Math:
		if entering {
			r.write(string(n.Literal))
		}
	case *ast.MathBlock:
		if entering {
			r.write(string(n.Literal))
			r.write("\n")
		}
		return ast.SkipChildren

	case *ast.Footnotes:
		return ast.SkipChildren

	}

	return ast.GoToNext
}
// Source: utils/markdown.ts table token handler
func (r *ansiRenderer) renderTable() {
	t := r.table
	r.table = nil // prevent write() from routing borders into cells

	// Collect all rows
	allRows := make([][]string, 0, 1+len(t.rows))
	if len(t.header) > 0 {
		allRows = append(allRows, t.header)
	}
	allRows = append(allRows, t.rows...)

	if len(allRows) == 0 {
		return
	}

	numCols := len(allRows[0])

	// Calculate column widths from visible text
	colWidths := make([]int, numCols)
	for _, row := range allRows {
		for i, cell := range row {
			if i < numCols {
				w := stringWidth(stripANSI(cell))
				if w > colWidths[i] {
					colWidths[i] = w
				}
			}
		}
	}
	for i := range colWidths {
		if colWidths[i] < 3 {
			colWidths[i] = 3
		}
	}

	// Render top border: ┌ ... ┬ ... ┐
	r.write("┌")
	for i := 0; i < numCols; i++ {
		r.write(strings.Repeat("─", colWidths[i]+2))
		if i < numCols-1 {
			r.write("┬")
		}
	}
	r.write("┐\n")

	// Render header row
	if len(t.header) > 0 {
		r.write("│ ")
		for i, cell := range t.header {
			if i < numCols {
				dw := stringWidth(stripANSI(cell))
				r.write(padAligned(cell, dw, colWidths[i], tableAlign(t.align, i)))
				r.write(" │")
				if i < numCols-1 {
					r.write(" ")
				}
			}
		}
		r.write("\n")

		// Separator row: ├───┼───┤ (or ┬ if single column)
		r.write("├")
		for i := 0; i < numCols; i++ {
			r.write(strings.Repeat("─", colWidths[i]+2))
			if i < numCols-1 {
				r.write("┼")
			}
		}
		r.write("┤\n")
	}

	// Data rows
	for _, row := range t.rows {
		r.write("│ ")
		for i, cell := range row {
			if i < numCols {
				dw := stringWidth(stripANSI(cell))
				r.write(padAligned(cell, dw, colWidths[i], tableAlign(t.align, i)))
				r.write(" │")
				if i < numCols-1 {
					r.write(" ")
				}
			}
		}
		r.write("\n")
	}

	// Render bottom border: └ ... ┴ ... ┘
	r.write("└")
	for i := 0; i < numCols; i++ {
		r.write(strings.Repeat("─", colWidths[i]+2))
		if i < numCols-1 {
			r.write("┴")
		}
	}
	r.write("┘")
}

func tableAlign(aligns []ast.CellAlignFlags, idx int) ast.CellAlignFlags {
	if aligns == nil || idx >= len(aligns) {
		return ast.CellAlignFlags(0)
	}
	return aligns[idx]
}

// highlightCode uses chroma to syntax-highlight code for terminal output.
func highlightCode(code, language string) string {
	lexer := lexers.Get(language)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	iterator, _ := lexer.Tokenise(nil, code)

	formatter := formatters.Get("terminal256")
	style := styles.Get("monokai")

	var buf bytes.Buffer
	_ = formatter.Format(&buf, style, iterator)
	return buf.String()
}

// ---- Numbering helpers (from TS markdown.ts) ----

func numberToLetter(n int) string {
	result := ""
	for n > 0 {
		n--
		result = string(rune('a'+(n%26))) + result
		n = n / 26
	}
	return result
}

var romanValues = []struct {
	value   int
	numeral string
}{
	{1000, "m"}, {900, "cm"}, {500, "d"}, {400, "cd"},
	{100, "c"}, {90, "xc"}, {50, "l"}, {40, "xl"},
	{10, "x"}, {9, "ix"}, {5, "v"}, {4, "iv"}, {1, "i"},
}

func numberToRoman(n int) string {
	result := ""
	for _, rv := range romanValues {
		for n >= rv.value {
			result += rv.numeral
			n -= rv.value
		}
	}
	return result
}

// getListNumber returns the formatted list number based on depth.
// depth 0-1: numbers (1, 2, 3)   depth 2: letters (a, b, c)   depth 3: roman (i, ii, iii)
func getListNumber(depth, num int) string {
	switch depth {
	case 0, 1:
		return fmt.Sprintf("%d", num)
	case 2:
		return numberToLetter(num)
	case 3:
		return numberToRoman(num)
	default:
		return fmt.Sprintf("%d", num)
	}
}

// padAligned pads content to targetWidth based on alignment.
// Source: utils/markdown.ts padAligned
func padAligned(content string, displayWidth, targetWidth int, align ast.CellAlignFlags) string {
	padding := targetWidth - displayWidth
	if padding < 0 {
		padding = 0
	}
	switch align {
	case ast.TableAlignmentCenter:
		leftPad := padding / 2
		return strings.Repeat(" ", leftPad) + content + strings.Repeat(" ", padding-leftPad)
	case ast.TableAlignmentRight:
		return strings.Repeat(" ", padding) + content
	default:
		return content + strings.Repeat(" ", padding)
	}
}

// ---- OSC 8 hyperlinks ----

// supportsHyperlinks returns true if the terminal likely supports OSC 8 hyperlinks.
func supportsHyperlinks() bool {
	return os.Getenv("TERM") != "dumb"
}

// createHyperlink wraps text in an OSC 8 terminal hyperlink.
// If text is empty, the URL is used as the display text.
func createHyperlink(url string, text ...string) string {
	const (
		osc8Start = "\x1b]8;;"
		osc8Sep   = "\x1b\\"
		osc8End   = "\x1b]8;;\x1b\\"
	)
	display := url
	if len(text) > 0 && text[0] != "" {
		display = text[0]
	}
	return osc8Start + url + osc8Sep + display + osc8End
}

// ---- Issue reference linkification (from TS markdown.ts) ----

// issueRefPattern matches owner/repo#NNN GitHub issue/PR references.
// Source: utils/markdown.ts ISSUE_REF_PATTERN
var issueRefPattern = regexp.MustCompile(`(^|[^\w./-])([A-Za-z0-9][\w-]*\/[A-Za-z0-9][\w.-]*)#(\d+)\b`)

// linkifyIssueReferences replaces owner/repo#123 with OSC 8 hyperlinks to GitHub.
func linkifyIssueReferences(text string) string {
	if !supportsHyperlinks() {
		return text
	}
	return issueRefPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := issueRefPattern.FindStringSubmatch(match)
		prefix := parts[1]
		repo := parts[2]
		num := parts[3]
		return prefix + createHyperlink(
			fmt.Sprintf("https://github.com/%s/issues/%s", repo, num),
			repo+"#"+num,
		)
	})
}

// ---- ANSI utilities ----

// stripANSI removes ANSI escape sequences from a string.
var stripANSI = tool.StripANSI

// stringWidth returns the display width of a string (excluding ANSI sequences).
// CJK characters count as 2 columns, matching TS stringWidth behavior.
func stringWidth(s string) int {
	clean := stripANSI(s)
	width := 0
	for _, r := range clean {
		width += runeDisplayWidth(r)
	}
	return width
}
