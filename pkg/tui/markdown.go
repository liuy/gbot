package tui

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	ast "github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/parser"
)

// Render converts markdown text to ANSI-styled terminal output.
// Source: utils/markdown.ts applyMarkdown → Go port
func Render(text string) string {
	extensions := parser.CommonExtensions | parser.AutoHeadingIDs | parser.Footnotes
	p := parser.NewWithExtensions(extensions)

	doc := p.Parse([]byte(text))
	if doc == nil {
		return text
	}

	var buf strings.Builder
	r := &ansiRenderer{
		w:          &buf,
		listStack:  []listCtx{},
		tableCells: nil,
	}

	ast.WalkFunc(doc, func(node ast.Node, entering bool) ast.WalkStatus {
		return r.renderNode(node, entering)
	})

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

// ansiRenderer walks the markdown AST and writes ANSI-styled output.
type ansiRenderer struct {
	w          io.Writer
	listStack  []listCtx
	tableCells []string // collects cells for current table row
	tableAlign []ast.CellAlignFlags
}

func (r *ansiRenderer) write(s string) {
	_, _ = r.w.Write([]byte(s))
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
	if len(r.listStack) == 0 {
		return nil
	}
	return &r.listStack[len(r.listStack)-1]
}

func (r *ansiRenderer) renderNode(node ast.Node, entering bool) ast.WalkStatus {
	switch n := node.(type) {

	// ---- Block nodes ----

	case *ast.Document:
		return ast.GoToNext

	case *ast.Heading:
		if entering {
			style := lipgloss.NewStyle().Bold(true)
			if n.Level == 1 {
				style = style.Italic(true).Underline(true)
			}
			r.write(style.Render(""))
		} else {
			r.write("\n\n")
		}

	case *ast.Paragraph:
		if !entering {
			r.write("\n")
		}

	case *ast.BlockQuote:
		if entering {
			r.write(lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render("│ "))
		} else {
			r.write("\n")
		}

	case *ast.List:
		if entering {
			ordered := n.ListFlags&ast.ListTypeOrdered != 0
			r.pushList(ordered)
		} else {
			r.popList()
			r.write("\n")
		}

	case *ast.ListItem:
		if entering {
			lc := r.currentList()
			if lc != nil {
				lc.counter++
				if lc.ordered {
					r.write(fmt.Sprintf("  %d. ", lc.counter))
				} else {
					r.write("  - ")
				}
			}
		} else {
			r.write("\n")
		}

	case *ast.CodeBlock:
		if entering {
			r.write(highlightCode(string(n.Literal), string(n.Info)))
			r.write("\n")
		}
		return ast.SkipChildren

	case *ast.HorizontalRule:
		if entering {
			r.write(lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render("───"))
			r.write("\n")
		}

	// ---- Table ----

	case *ast.Table:
		if entering {
			r.tableCells = nil
		} else {
			r.tableCells = nil
			r.tableAlign = nil
			r.write("\n")
		}

	case *ast.TableHeader:
		// collect alignment info from first header row
		return ast.GoToNext

	case *ast.TableBody:
		return ast.GoToNext

	case *ast.TableRow:
		if entering {
			r.tableCells = r.tableCells[:0]
		} else {
			// Render the row
			if len(r.tableCells) > 0 {
				r.write("| ")
				for _, cell := range r.tableCells {
					r.write(cell)
					r.write(" | ")
				}
				r.write("\n")
			}
		}

	case *ast.TableCell:
		if entering {
			r.tableCells = append(r.tableCells, "")
		}
		// On leaving, cell text was already appended by Text children

	// ---- Inline nodes ----

	case *ast.Text:
		if entering {
			content := string(n.Literal)
			// If inside a TableCell, append to the cell buffer
			if len(r.tableCells) > 0 {
				r.tableCells[len(r.tableCells)-1] += content
			} else {
				r.write(content)
			}
		}

	case *ast.Softbreak:
		if entering {
			r.write("\n")
		}

	case *ast.Hardbreak:
		if entering {
			r.write("\n")
		}

	case *ast.Code:
		if entering {
			codeStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("15")).
				Background(lipgloss.Color("62"))
			r.write(codeStyle.Render(string(n.Literal)))
		}

	case *ast.Emph:
		if entering {
			r.write(lipgloss.NewStyle().Italic(true).Render(""))
		}

	case *ast.Strong:
		if entering {
			r.write(lipgloss.NewStyle().Bold(true).Render(""))
		}

	case *ast.Del:
		if entering {
			r.write(lipgloss.NewStyle().Strikethrough(true).Render(""))
		}

	case *ast.Link:
		if entering {
			r.write(lipgloss.NewStyle().
				Foreground(lipgloss.Color("12")).
				Underline(true).
				Render(""))
		} else {
			if len(n.Destination) > 0 {
				dest := string(n.Destination)
				r.write(lipgloss.NewStyle().
					Foreground(lipgloss.Color("8")).
					Render(fmt.Sprintf(" (%s)", dest)))
			}
		}

	case *ast.Image:
		if entering {
			r.write(lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render(string(n.Destination)))
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

	case *ast.Caption:
	case *ast.CaptionFigure:
	case *ast.Callout:
	}

	return ast.GoToNext
}

// highlightCode uses chroma to syntax-highlight code for terminal output.
func highlightCode(code, language string) string {
	lexer := lexers.Get(language)
	if lexer == nil {
		lexer = lexers.Fallback
	}

	// Ensure coalesce is set
	type coalescer interface{ SetCoalesce(bool) }
	if c, ok := lexer.(coalescer); ok {
		c.SetCoalesce(true)
	}

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}

	formatter := formatters.Get("terminal256")
	if formatter == nil {
		formatter = formatters.Fallback
	}

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return code
	}
	return buf.String()
}
