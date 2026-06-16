// Copyright 2026 Conductor OSS
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
// the License. You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
// an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
// specific language governing permissions and limitations under the License.

package markitdown

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestEscapeLatex(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "hello", "hello"},
		{"dollar", "$x$", "\\$x\\$"},
		{"underscore", "a_b", "a\\_b"},
		{"ampersand", "a&b", "a\\&b"},
		{"percent", "50%", "50\\%"},
		{"hash", "#1", "\\#1"},
		{"caret", "x^2", "x\\^2"},
		{"braces", "{x}", "\\{x\\}"},
		{"tilde", "x~y", "x\\~y"},
		{"already_escaped", `a\$b`, "a\\$b"},
		{"multiple_specials", "$_a", "\\$\\_a"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EscapeLatex(tt.input)
			if got != tt.want {
				t.Errorf("EscapeLatex(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetVal(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		defaultVal string
		store      map[string]string
		want       string
	}{
		{"empty_key_returns_default", "", "def", map[string]string{"a": "b"}, "def"},
		{"key_in_store", "k", "def", map[string]string{"k": "v"}, "v"},
		{"key_not_in_store", "k", "def", map[string]string{"a": "b"}, "k"},
		{"nil_store_returns_key", "k", "def", nil, "k"},
		{"empty_key_empty_default", "", "", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getVal(tt.key, tt.defaultVal, tt.store)
			if got != tt.want {
				t.Errorf("getVal(%q, %q, %v) = %q, want %q",
					tt.key, tt.defaultVal, tt.store, got, tt.want)
			}
		})
	}
}

func TestFormatTemplate(t *testing.T) {
	got := formatTemplate("{a} and {b}", map[string]string{"a": "X", "b": "Y"})
	want := "X and Y"
	if got != want {
		t.Errorf("formatTemplate = %q, want %q", got, want)
	}

	got = formatTemplate("no placeholders", map[string]string{"a": "X"})
	if got != "no placeholders" {
		t.Errorf("formatTemplate no placeholders = %q, want %q", got, "no placeholders")
	}

	got = formatTemplate("{missing}", map[string]string{"a": "X"})
	if got != "{missing}" {
		t.Errorf("formatTemplate missing key = %q, want %q", got, "{missing}")
	}
}

func TestFormatPositional(t *testing.T) {
	got := formatPositional("prefix {0} suffix", "VALUE")
	want := "prefix VALUE suffix"
	if got != want {
		t.Errorf("formatPositional = %q, want %q", got, want)
	}

	got = formatPositional("{0} and {0}", "X")
	if got != "X and X" {
		t.Errorf("formatPositional repeated = %q, want %q", got, "X and X")
	}

	got = formatPositional("nothing", "X")
	if got != "nothing" {
		t.Errorf("formatPositional no placeholder = %q, want %q", got, "nothing")
	}
}

func xmlNameLocal(local string) xml.Name {
	return xml.Name{Local: local}
}

func xmlAttrVal(value string) xml.Attr {
	return xml.Attr{
		Name:  xml.Name{Local: "val"},
		Value: value,
	}
}

func TestOMMLElementLocalName(t *testing.T) {
	e := &OMMLElement{XMLName: xmlNameLocal("testTag")}
	if got := e.localName(); got != "testTag" {
		t.Errorf("localName() = %q, want %q", got, "testTag")
	}
}

func TestOMMLElementFindChild(t *testing.T) {
	parent := &OMMLElement{}
	parent.Children = []OMMLElement{
		{XMLName: xmlNameLocal("first")},
		{XMLName: xmlNameLocal("second")},
		{XMLName: xmlNameLocal("third")},
	}

	c := parent.findChild("second")
	if c == nil {
		t.Fatal("findChild(second) returned nil")
	}
	if c.localName() != "second" {
		t.Errorf("findChild returned %q, want %q", c.localName(), "second")
	}

	c = parent.findChild("missing")
	if c != nil {
		t.Errorf("findChild(missing) should return nil, got %v", c)
	}
}

func TestOMMLElementGetAttrVal(t *testing.T) {
	e := &OMMLElement{}
	e.Attrs = []xml.Attr{
		{Name: xml.Name{Local: "other"}, Value: "v1"},
		{Name: xml.Name{Local: "val"}, Value: "theval"},
	}
	if got := e.getAttrVal(); got != "theval" {
		t.Errorf("getAttrVal() = %q, want %q", got, "theval")
	}

	e2 := &OMMLElement{}
	e2.Attrs = []xml.Attr{{Name: xml.Name{Local: "x"}, Value: "y"}}
	if got := e2.getAttrVal(); got != "" {
		t.Errorf("getAttrVal() with no val attr = %q, want empty", got)
	}
}

func TestParsePr(t *testing.T) {
	parent := &OMMLElement{}
	parent.Children = []OMMLElement{
		{XMLName: xmlNameLocal("brk")},
		{XMLName: xmlNameLocal("chr"), Attrs: []xml.Attr{xmlAttrVal("X")}},
		{XMLName: xmlNameLocal("pos"), Attrs: []xml.Attr{xmlAttrVal("top")}},
		{XMLName: xmlNameLocal("begChr"), Attrs: []xml.Attr{xmlAttrVal("(")}},
		{XMLName: xmlNameLocal("endChr"), Attrs: []xml.Attr{xmlAttrVal(")")}},
		{XMLName: xmlNameLocal("type"), Attrs: []xml.Attr{xmlAttrVal("bar")}},
		{XMLName: xmlNameLocal("unknown")},
	}
	pr := parsePr(parent)
	if pr.brk != Brk {
		t.Errorf("brk = %q, want %q", pr.brk, Brk)
	}
	if pr.chr != "X" {
		t.Errorf("chr = %q, want X", pr.chr)
	}
	if pr.pos != "top" {
		t.Errorf("pos = %q, want top", pr.pos)
	}
	if pr.begChr != "(" {
		t.Errorf("begChr = %q, want (", pr.begChr)
	}
	if pr.endChr != ")" {
		t.Errorf("endChr = %q, want )", pr.endChr)
	}
	if pr.typ != "bar" {
		t.Errorf("typ = %q, want bar", pr.typ)
	}
	if !strings.Contains(pr.text, Brk) {
		t.Errorf("text = %q, should contain Brk %q", pr.text, Brk)
	}
}

func TestConvertOMMLString(t *testing.T) {
	xmlInput := `<m:oMath><m:r><m:t>x</m:t></m:r></m:oMath>`
	results, err := ConvertOMMLString(xmlInput)
	if err != nil {
		t.Fatalf("ConvertOMMLString error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0] != "x" {
		t.Errorf("result = %q, want x", results[0])
	}
}

func TestConvertOMMLStringInvalidXML(t *testing.T) {
	_, err := ConvertOMMLString("<<<not xml>>>")
	if err == nil {
		t.Fatal("expected error for invalid XML, got nil")
	}
	if !strings.Contains(err.Error(), "parse OMML") {
		t.Errorf("error = %v, should contain 'parse OMML'", err)
	}
}

func TestConvertOMMLStringMultiple(t *testing.T) {
	xmlInput := `<m:oMath><m:r><m:t>a</m:t></m:r></m:oMath><m:oMath><m:r><m:t>b</m:t></m:r></m:oMath>`
	results, err := ConvertOMMLString(xmlInput)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0] != "a" || results[1] != "b" {
		t.Errorf("results = %v, want [a b]", results)
	}
}

func TestConvertOMMLStringNoOMath(t *testing.T) {
	xmlInput := `<m:other>text</m:other>`
	results, err := ConvertOMMLString(xmlInput)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d: %v", len(results), results)
	}
}

// mkR returns an OMMLElement representing <m:r><m:t>content</m:t></m:r>.
func mkR(content string) OMMLElement {
	return OMMLElement{
		XMLName: xmlNameLocal("r"),
		Children: []OMMLElement{
			{XMLName: xmlNameLocal("t"), Content: content},
		},
	}
}

func TestProcessChildrenWithInclude(t *testing.T) {
	// Use "e" (element) children which are directTags and will recurse into their r/t children
	parent := &OMMLElement{}
	parent.Children = []OMMLElement{
		{XMLName: xmlNameLocal("e"), Children: []OMMLElement{mkR("X")}},
		{XMLName: xmlNameLocal("num"), Children: []OMMLElement{mkR("Y")}},
		{XMLName: xmlNameLocal("den"), Children: []OMMLElement{mkR("Z")}},
	}
	include := map[string]bool{"e": true, "den": true}
	got := processChildren(parent, include)
	if got != "XZ" {
		t.Errorf("processChildren = %q, want XZ", got)
	}
}

func TestProcessChildrenDictWithInclude(t *testing.T) {
	parent := &OMMLElement{}
	parent.Children = []OMMLElement{
		{XMLName: xmlNameLocal("e"), Children: []OMMLElement{mkR("X")}},
		{XMLName: xmlNameLocal("num"), Children: []OMMLElement{mkR("Y")}},
	}
	include := map[string]bool{"e": true}
	got := processChildrenDict(parent, include)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got["e"] != "X" {
		t.Errorf("got[e] = %q, want X", got["e"])
	}
	if _, ok := got["num"]; ok {
		t.Errorf("should not include num")
	}
}

func TestProcessChildrenListWithInclude(t *testing.T) {
	parent := &OMMLElement{}
	parent.Children = []OMMLElement{
		{XMLName: xmlNameLocal("e"), Children: []OMMLElement{mkR("X")}},
		{XMLName: xmlNameLocal("num"), Children: []OMMLElement{mkR("Y")}},
	}
	include := map[string]bool{"e": true}
	got := processChildrenList(parent, include)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].tag != "e" || got[0].text != "X" {
		t.Errorf("got[0] = {%q, %q}, want {e, X}", got[0].tag, got[0].text)
	}
}

func TestProcessElementUnknownTag(t *testing.T) {
	e := &OMMLElement{XMLName: xmlNameLocal("unknownTag")}
	if got := processElement(e); got != "" {
		t.Errorf("processElement(unknown) = %q, want empty", got)
	}

	e = &OMMLElement{
		XMLName: xmlNameLocal("unkPr"),
		Children: []OMMLElement{
			{XMLName: xmlNameLocal("brk")},
		},
	}
	got := processElement(e)
	if !strings.Contains(got, Brk) {
		t.Errorf("processElement(*Pr) = %q, should contain Brk", got)
	}

	// "box" is in directTags, so processElement recurses into its children
	e = &OMMLElement{
		XMLName: xmlNameLocal("box"),
		Children: []OMMLElement{
			{XMLName: xmlNameLocal("r"), Children: []OMMLElement{{XMLName: xmlNameLocal("t"), Content: "hi"}}},
		},
	}
	got = processElement(e)
	if got != "hi" {
		t.Errorf("processElement(box) = %q, want hi", got)
	}
}

func TestDoR(t *testing.T) {
	e := &OMMLElement{
		XMLName: xmlNameLocal("r"),
		Children: []OMMLElement{
			{XMLName: xmlNameLocal("t"), Content: "abc"},
		},
	}
	got := doR(e)
	if got != "abc" {
		t.Errorf("doR = %q, want abc", got)
	}

	e2 := &OMMLElement{XMLName: xmlNameLocal("r")}
	if got := doR(e2); got != "" {
		t.Errorf("doR without t = %q, want empty", got)
	}

	e3 := &OMMLElement{
		XMLName: xmlNameLocal("r"),
		Children: []OMMLElement{
			{XMLName: xmlNameLocal("t"), Content: "\u2192"},
		},
	}
	got = doR(e3)
	if !strings.Contains(got, "\\rightarrow") {
		t.Errorf("doR with arrow = %q, should contain \\rightarrow", got)
	}
}

// wrapOMath wraps an inner OMML XML snippet inside <m:oMath> for ConvertOMMLString.
func wrapOMath(inner string) string {
	return "<m:oMath>" + inner + "</m:oMath>"
}

// convertSingle is a test helper that wraps inner XML in oMath and returns the single result.
func convertSingle(t *testing.T, inner string) string {
	t.Helper()
	results, err := ConvertOMMLString(wrapOMath(inner))
	if err != nil {
		t.Fatalf("ConvertOMMLString error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	return results[0]
}

func TestDoAcc(t *testing.T) {
	got := convertSingle(t, `<m:acc><m:accPr><m:chr m:val="&#x0303;"/></m:accPr><m:e><m:r><m:t>x</m:t></m:r></m:e></m:acc>`)
	if !strings.Contains(got, "tilde") {
		t.Errorf("doAcc with tilde chr = %q, should contain tilde", got)
	}
	if !strings.Contains(got, "x") {
		t.Errorf("doAcc = %q, should contain x", got)
	}
}

func TestDoAccDefault(t *testing.T) {
	// accPr present but without chr child - falls back to default hat
	got := convertSingle(t, `<m:acc><m:accPr></m:accPr><m:e><m:r><m:t>y</m:t></m:r></m:e></m:acc>`)
	if !strings.Contains(got, "hat") {
		t.Errorf("doAcc default = %q, should contain hat", got)
	}
}

func TestDoBar(t *testing.T) {
	got := convertSingle(t, `<m:bar><m:barPr></m:barPr><m:e><m:r><m:t>z</m:t></m:r></m:e></m:bar>`)
	if !strings.Contains(got, "overline") {
		t.Errorf("doBar default = %q, should contain overline", got)
	}
}

func TestDoBarBot(t *testing.T) {
	got := convertSingle(t, `<m:bar><m:barPr><m:pos m:val="bot"/></m:barPr><m:e><m:r><m:t>z</m:t></m:r></m:e></m:bar>`)
	if !strings.Contains(got, "underline") {
		t.Errorf("doBar bot = %q, should contain underline", got)
	}
}

func TestDoD(t *testing.T) {
	got := convertSingle(t, `<m:d><m:dPr></m:dPr><m:e><m:r><m:t>content</m:t></m:r></m:e></m:d>`)
	if !strings.Contains(got, "\\left(") {
		t.Errorf("doD default = %q, should contain \\left(", got)
	}
	if !strings.Contains(got, "\\right)") {
		t.Errorf("doD default = %q, should contain \\right)", got)
	}
}

func TestDoDCustomDelimiters(t *testing.T) {
	got := convertSingle(t, `<m:d><m:dPr><m:begChr m:val="["/><m:endChr m:val="]"/></m:dPr><m:e><m:r><m:t>x</m:t></m:r></m:e></m:d>`)
	if !strings.Contains(got, "\\left[") {
		t.Errorf("doD custom = %q, should contain \\left[", got)
	}
	if !strings.Contains(got, "\\right]") {
		t.Errorf("doD custom = %q, should contain \\right]", got)
	}
}

func TestDoSub(t *testing.T) {
	got := convertSingle(t, `<m:sSub><m:e><m:r><m:t>x</m:t></m:r></m:e><m:sub><m:r><m:t>i</m:t></m:r></m:sub></m:sSub>`)
	// SUB template is "_{{{0}}}" which produces _{...} with nested content
	if !strings.Contains(got, "_") {
		t.Errorf("doSub = %q, should contain underscore", got)
	}
	if !strings.Contains(got, "x") {
		t.Errorf("doSub = %q, should contain base x", got)
	}
	if !strings.Contains(got, "i") {
		t.Errorf("doSub = %q, should contain subscript i", got)
	}
}

func TestDoSup(t *testing.T) {
	got := convertSingle(t, `<m:sSup><m:e><m:r><m:t>x</m:t></m:r></m:e><m:sup><m:r><m:t>2</m:t></m:r></m:sup></m:sSup>`)
	if !strings.Contains(got, "^") {
		t.Errorf("doSup = %q, should contain caret", got)
	}
	if !strings.Contains(got, "x") {
		t.Errorf("doSup = %q, should contain base x", got)
	}
	if !strings.Contains(got, "2") {
		t.Errorf("doSup = %q, should contain superscript 2", got)
	}
}

func TestDoF(t *testing.T) {
	got := convertSingle(t, `<m:f><m:fPr></m:fPr><m:num><m:r><m:t>a</m:t></m:r></m:num><m:den><m:r><m:t>b</m:t></m:r></m:den></m:f>`)
	if !strings.Contains(got, "\\frac") {
		t.Errorf("doF = %q, should contain \\frac", got)
	}
	if !strings.Contains(got, "{a}") {
		t.Errorf("doF = %q, should contain {a}", got)
	}
	if !strings.Contains(got, "{b}") {
		t.Errorf("doF = %q, should contain {b}", got)
	}
}

func TestDoFLinear(t *testing.T) {
	got := convertSingle(t, `<m:f><m:fPr><m:type m:val="lin"/></m:fPr><m:num><m:r><m:t>a</m:t></m:r></m:num><m:den><m:r><m:t>b</m:t></m:r></m:den></m:f>`)
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("doF lin = %q, should contain a and b", got)
	}
	if !strings.Contains(got, "/") {
		t.Errorf("doF lin = %q, should contain / for linear fraction", got)
	}
}

func TestDoFunc(t *testing.T) {
	got := convertSingle(t, `<m:func><m:fName><m:r><m:t>sin</m:t></m:r></m:fName><m:e><m:r><m:t>x</m:t></m:r></m:e></m:func>`)
	if !strings.Contains(got, "\\sin") {
		t.Errorf("doFunc sin = %q, should contain \\sin", got)
	}
}

func TestDoFNameUnknown(t *testing.T) {
	got := convertSingle(t, `<m:func><m:fName><m:r><m:t>customfunc</m:t></m:r></m:fName><m:e><m:r><m:t>x</m:t></m:r></m:e></m:func>`)
	if !strings.Contains(got, "customfunc") {
		t.Errorf("doFName unknown = %q, should contain customfunc", got)
	}
}

func TestDoGroupChr(t *testing.T) {
	got := convertSingle(t, `<m:groupChr><m:groupChrPr><m:chr m:val="&#x23DF;"/></m:groupChrPr><m:e><m:r><m:t>xyz</m:t></m:r></m:e></m:groupChr>`)
	if !strings.Contains(got, "underbrace") {
		t.Errorf("doGroupChr = %q, should contain underbrace", got)
	}
}

func TestDoRad(t *testing.T) {
	got := convertSingle(t, `<m:rad><m:radPr></m:radPr><m:e><m:r><m:t>x</m:t></m:r></m:e></m:rad>`)
	if !strings.Contains(got, "\\sqrt") {
		t.Errorf("doRad default = %q, should contain \\sqrt", got)
	}
	if !strings.Contains(got, "x") {
		t.Errorf("doRad default = %q, should contain x", got)
	}
}

func TestDoRadWithDeg(t *testing.T) {
	got := convertSingle(t, `<m:rad><m:deg><m:r><m:t>3</m:t></m:r></m:deg><m:e><m:r><m:t>x</m:t></m:r></m:e></m:rad>`)
	if !strings.Contains(got, "\\sqrt[3]") {
		t.Errorf("doRad with deg = %q, should contain \\sqrt[3]", got)
	}
}

func TestDoEqArr(t *testing.T) {
	got := convertSingle(t, `<m:eqArr><m:e><m:r><m:t>a</m:t></m:r></m:e><m:e><m:r><m:t>b</m:t></m:r></m:e></m:eqArr>`)
	if !strings.Contains(got, "array") {
		t.Errorf("doEqArr = %q, should contain array", got)
	}
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("doEqArr = %q, should contain a and b", got)
	}
}

func TestDoLimLow(t *testing.T) {
	got := convertSingle(t, `<m:limLow><m:e><m:r><m:t>lim</m:t></m:r></m:e><m:lim><m:r><m:t>n&#x2192;&#x221E;</m:t></m:r></m:lim></m:limLow>`)
	if !strings.Contains(got, "\\lim_") {
		t.Errorf("doLimLow lim = %q, should contain \\lim_", got)
	}
}

func TestDoLimLowGeneric(t *testing.T) {
	got := convertSingle(t, `<m:limLow><m:e><m:r><m:t>custom</m:t></m:r></m:e><m:lim><m:r><m:t>x</m:t></m:r></m:lim></m:limLow>`)
	if !strings.Contains(got, "_{x}") {
		t.Errorf("doLimLow generic = %q, should contain _{x}", got)
	}
}

func TestDoLimUpp(t *testing.T) {
	got := convertSingle(t, `<m:limUpp><m:e><m:r><m:t>x</m:t></m:r></m:e><m:lim><m:r><m:t>n</m:t></m:r></m:lim></m:limUpp>`)
	if !strings.Contains(got, "\\overset") {
		t.Errorf("doLimUpp = %q, should contain \\overset", got)
	}
}

func TestDoLim(t *testing.T) {
	got := convertSingle(t, `<m:lim><m:r><m:t>&#x2192;</m:t></m:r></m:lim>`)
	if !strings.Contains(got, "\\to") {
		t.Errorf("doLim = %q, should contain \\to", got)
	}
	if strings.Contains(got, "\\rightarrow") {
		t.Errorf("doLim = %q, should NOT contain \\rightarrow", got)
	}
}

func TestDoM(t *testing.T) {
	got := convertSingle(t, `<m:m><m:mr><m:e><m:r><m:t>a</m:t></m:r></m:e><m:e><m:r><m:t>b</m:t></m:r></m:e></m:mr><m:mr><m:e><m:r><m:t>c</m:t></m:r></m:e><m:e><m:r><m:t>d</m:t></m:r></m:e></m:mr></m:m>`)
	if !strings.Contains(got, "matrix") {
		t.Errorf("doM = %q, should contain matrix", got)
	}
	if !strings.Contains(got, "a") || !strings.Contains(got, "d") {
		t.Errorf("doM = %q, should contain a and d", got)
	}
}

func TestDoMr(t *testing.T) {
	parent := &OMMLElement{XMLName: xmlNameLocal("mr")}
	parent.Children = []OMMLElement{
		{XMLName: xmlNameLocal("e"), Children: []OMMLElement{{XMLName: xmlNameLocal("r"), Children: []OMMLElement{{XMLName: xmlNameLocal("t"), Content: "x"}}}}},
		{XMLName: xmlNameLocal("e"), Children: []OMMLElement{{XMLName: xmlNameLocal("r"), Children: []OMMLElement{{XMLName: xmlNameLocal("t"), Content: "y"}}}}},
	}
	got := doMr(parent)
	if !strings.Contains(got, "x") || !strings.Contains(got, "y") {
		t.Errorf("doMr = %q, should contain x and y", got)
	}
	if !strings.Contains(got, "&") {
		t.Errorf("doMr = %q, should contain & separator", got)
	}
}

func TestDoNary(t *testing.T) {
	got := convertSingle(t, `<m:nary><m:naryPr><m:chr m:val="&#x2211;"/></m:naryPr><m:sub><m:r><m:t>i=0</m:t></m:r></m:sub><m:sup><m:r><m:t>n</m:t></m:r></m:sup><m:e><m:r><m:t>x</m:t></m:r></m:e></m:nary>`)
	// doNary processes sub/sup/e children
	if !strings.Contains(got, "i=0") {
		t.Errorf("doNary = %q, should contain subscript i=0", got)
	}
	if !strings.Contains(got, "n") {
		t.Errorf("doNary = %q, should contain superscript n", got)
	}
	if !strings.Contains(got, "x") {
		t.Errorf("doNary = %q, should contain body x", got)
	}
}

func TestDoNaryDefault(t *testing.T) {
	got := convertSingle(t, `<m:nary><m:sub><m:r><m:t>i</m:t></m:r></m:sub><m:e><m:r><m:t>x</m:t></m:r></m:e></m:nary>`)
	if got == "" {
		t.Errorf("doNary default should produce non-empty output")
	}
}

func TestConvertOMML(t *testing.T) {
	e := &OMMLElement{
		XMLName: xmlNameLocal("oMath"),
		Children: []OMMLElement{
			{XMLName: xmlNameLocal("r"), Children: []OMMLElement{{XMLName: xmlNameLocal("t"), Content: "test"}}},
		},
	}
	got := ConvertOMML(e)
	if got != "test" {
		t.Errorf("ConvertOMML = %q, want test", got)
	}
}
