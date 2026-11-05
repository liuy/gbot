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
	"errors"
	"testing"
)

func TestEscapeLatexCollapsesDoubleBackslash(t *testing.T) {
	if got := EscapeLatex(`\\`); got != `\` {
		t.Errorf("EscapeLatex(%q) = %q, want %q", `\\`, got, `\`)
	}
}

func TestConvertOMMLStringInvalidPreservesCause(t *testing.T) {
	_, err := ConvertOMMLString("<<<not omml")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if _, ok := errors.AsType[*xml.SyntaxError](err); !ok {
		t.Errorf("parse error must wrap xml.SyntaxError via %%w, got: %v", err)
	}
}

// Each directTags member renders its children verbatim.
func TestDirectTagsRenderChildren(t *testing.T) {
	cases := []struct {
		inner string
		want  string
	}{
		{`<m:box><m:r><m:t>bx</m:t></m:r></m:box>`, "bx"},
		{`<m:sSub><m:e><m:r><m:t>E</m:t></m:r></m:e><m:sub><m:r><m:t>S</m:t></m:r></m:sub></m:sSub>`, `E_{{S}}`},
		{`<m:sSup><m:e><m:r><m:t>E</m:t></m:r></m:e><m:sup><m:r><m:t>P</m:t></m:r></m:sup></m:sSup>`, `E^{{P}}`},
		{`<m:sSubSup><m:e><m:r><m:t>E</m:t></m:r></m:e><m:sub><m:r><m:t>S</m:t></m:r></m:sub><m:sup><m:r><m:t>P</m:t></m:r></m:sup></m:sSubSup>`, `E_{{S}}^{{P}}`},
		{`<m:num><m:r><m:t>N</m:t></m:r></m:num>`, "N"},
		{`<m:den><m:r><m:t>D</m:t></m:r></m:den>`, "D"},
		{`<m:deg><m:r><m:t>G</m:t></m:r></m:deg>`, "G"},
		{`<m:e><m:r><m:t>EE</m:t></m:r></m:e>`, "EE"},
	}
	for _, tc := range cases {
		if got := convertSingle(t, tc.inner); got != tc.want {
			t.Errorf("convertSingle(%s) = %q, want %q", tc.inner, got, tc.want)
		}
	}
}

func TestDoBarRendersBody(t *testing.T) {
	got := convertSingle(t, `<m:bar><m:barPr/><m:e><m:r><m:t>B</m:t></m:r></m:e></m:bar>`)
	if got != `\overline{{B}}` {
		t.Errorf("doBar = %q, want %q", got, `\overline{{B}}`)
	}
}

func TestDoBarBrkPrefix(t *testing.T) {
	got := convertSingle(t, `<m:bar><m:barPr><m:brk/></m:barPr><m:e><m:r><m:t>B</m:t></m:r></m:e></m:bar>`)
	if got != `\\\overline{{B}}` {
		t.Errorf("doBar with brk = %q, want %q", got, `\\\overline{{B}}`)
	}
}

func TestDoDRendersBody(t *testing.T) {
	got := convertSingle(t, `<m:d><m:dPr/><m:e><m:r><m:t>x</m:t></m:r></m:e></m:d>`)
	if got != `\left(x\right)` {
		t.Errorf("doD = %q, want %q", got, `\left(x\right)`)
	}
}

func TestDoDCustomEndDelimiter(t *testing.T) {
	got := convertSingle(t, `<m:d><m:dPr><m:endChr m:val="]"/></m:dPr><m:e><m:r><m:t>x</m:t></m:r></m:e></m:d>`)
	if got != `\left(x\right]` {
		t.Errorf("doD custom end = %q, want %q", got, `\left(x\right]`)
	}
}

func TestDoFRendersFraction(t *testing.T) {
	got := convertSingle(t, `<m:f><m:fPr/><m:num><m:r><m:t>1</m:t></m:r></m:num><m:den><m:r><m:t>2</m:t></m:r></m:den></m:f>`)
	if got != `\frac{{1}}{{2}}` {
		t.Errorf("doF = %q, want %q", got, `\frac{{1}}{{2}}`)
	}
}

func TestDoGroupChrRendersVector(t *testing.T) {
	got := convertSingle(t, `<m:groupChr><m:groupChrPr><m:chr m:val="&#x20d7;"/></m:groupChrPr><m:e><m:r><m:t>G</m:t></m:r></m:e></m:groupChr>`)
	if got != `\vec{{G}}` {
		t.Errorf("doGroupChr = %q, want %q", got, `\vec{{G}}`)
	}
}

func TestDoFNameUnknownFunc(t *testing.T) {
	got := convertSingle(t, `<m:func><m:fName><m:r><m:t>foo</m:t></m:r></m:fName><m:e><m:r><m:t>x</m:t></m:r></m:e></m:func>`)
	if got != "foox" {
		t.Errorf("doFunc unknown = %q, want foox", got)
	}
}

func TestDoRadExact(t *testing.T) {
	if got := convertSingle(t, `<m:rad><m:radPr/><m:deg><m:r><m:t>2</m:t></m:r></m:deg><m:e><m:r><m:t>x</m:t></m:r></m:e></m:rad>`); got != `\sqrt[2]{{x}}` {
		t.Errorf("doRad deg = %q, want %q", got, `\sqrt[2]{{x}}`)
	}
	if got := convertSingle(t, `<m:rad><m:radPr/><m:e><m:r><m:t>x</m:t></m:r></m:e></m:rad>`); got != `\sqrt{{x}}` {
		t.Errorf("doRad = %q, want %q", got, `\sqrt{{x}}`)
	}
}

// Non-e children are filtered out of equation arrays.
func TestDoEqArrFiltersToE(t *testing.T) {
	got := convertSingle(t, `<m:eqArr><m:e><m:r><m:t>a</m:t></m:r></m:e><m:sub><m:r><m:t>j</m:t></m:r></m:sub></m:eqArr>`)
	if got != `\begin{{array}}{{c}}a\end{{array}}` {
		t.Errorf("doEqArr = %q, want %q", got, `\begin{{array}}{{c}}a\end{{array}}`)
	}
}

func TestDoEqArrRowsJoined(t *testing.T) {
	got := convertSingle(t, `<m:eqArr><m:e><m:r><m:t>a</m:t></m:r></m:e><m:e><m:r><m:t>b</m:t></m:r></m:e></m:eqArr>`)
	if got != `\begin{{array}}{{c}}a\\b\end{{array}}` {
		t.Errorf("doEqArr rows = %q, want %q", got, `\begin{{array}}{{c}}a\\b\end{{array}}`)
	}
}

func TestDoLimLowExact(t *testing.T) {
	if got := convertSingle(t, `<m:limLow><m:e><m:r><m:t>lim</m:t></m:r></m:e><m:lim><m:r><m:t>n</m:t></m:r></m:lim></m:limLow>`); got != `\lim_{{n}}` {
		t.Errorf("doLimLow = %q, want %q", got, `\lim_{{n}}`)
	}
	if got := convertSingle(t, `<m:limLow><m:e><m:r><m:t>max</m:t></m:r></m:e><m:lim><m:r><m:t>0</m:t></m:r></m:lim></m:limLow>`); got != `\max_{{0}}` {
		t.Errorf("doLimLow max = %q, want %q", got, `\max_{{0}}`)
	}
}

func TestDoLimUppExact(t *testing.T) {
	got := convertSingle(t, `<m:limUpp><m:e><m:r><m:t>X</m:t></m:r></m:e><m:lim><m:r><m:t>n</m:t></m:r></m:lim></m:limUpp>`)
	if got != `\overset{{n}}{{X}}` {
		t.Errorf("doLimUpp = %q, want %q", got, `\overset{{n}}{{X}}`)
	}
}

// The mPr child of a matrix is skipped; rows come only from mr children.
func TestDoMSkipsMPr(t *testing.T) {
	if got := convertSingle(t, `<m:m><m:mPr/><m:mr><m:e><m:r><m:t>a</m:t></m:r></m:e><m:e><m:r><m:t>b</m:t></m:r></m:e></m:mr><m:mr><m:e><m:r><m:t>c</m:t></m:r></m:e></m:mr></m:m>`); got != `\begin{{matrix}}a&b\\c\end{{matrix}}` {
		t.Errorf("doM = %q, want %q", got, `\begin{{matrix}}a&b\\c\end{{matrix}}`)
	}
	if got := convertSingle(t, `<m:m><m:mPr><m:brk/></m:mPr><m:mr><m:e><m:r><m:t>a</m:t></m:r></m:e></m:mr></m:m>`); got != `\begin{{matrix}}a\end{{matrix}}` {
		t.Errorf("doM with brk mPr = %q, want %q", got, `\begin{{matrix}}a\end{{matrix}}`)
	}
}

// The naryPr big-operator character is applied when the property element has
// visible text (a brk), which is the only shape that reaches the lookup.
func TestDoNaryBigOperator(t *testing.T) {
	got := convertSingle(t, `<m:nary><m:naryPr><m:brk/><m:chr m:val="&#x2211;"/></m:naryPr><m:e><m:r><m:t>i</m:t></m:r></m:e></m:nary>`)
	if got != `\sumi` {
		t.Errorf("doNary big operator = %q, want %q", got, `\sumi`)
	}
}

// The include filter in processChildrenDict must skip-and-continue, not
// stop: a leading mPr child must not hide the e/lim that follow.
func TestDoLimLowSkipsLeadingMPr(t *testing.T) {
	got := convertSingle(t, `<m:limLow><m:mPr/><m:e><m:r><m:t>lim</m:t></m:r></m:e><m:lim><m:r><m:t>n</m:t></m:r></m:lim></m:limLow>`)
	if got != `\lim_{{n}}` {
		t.Errorf("doLimLow with leading mPr = %q, want %q", got, `\lim_{{n}}`)
	}
}

// A leading excluded child must not stop the include filter in
// processChildrenList: eqArr rows after an mPr still render.
func TestDoEqArrSkipsLeadingMPr(t *testing.T) {
	got := convertSingle(t, `<m:eqArr><m:mPr/><m:e><m:r><m:t>a</m:t></m:r></m:e><m:e><m:r><m:t>b</m:t></m:r></m:e></m:eqArr>`)
	if got != `\begin{{array}}{{c}}a\\b\end{{array}}` {
		t.Errorf("doEqArr with leading mPr = %q, want %q", got, `\begin{{array}}{{c}}a\\b\end{{array}}`)
	}
}

// Non-r children of fName contribute their text through the fallback arm.
func TestDoFNameNonRunChild(t *testing.T) {
	got := convertSingle(t, `<m:func><m:fName><m:r><m:t>foo</m:t></m:r><m:e><m:r><m:t>bar</m:t></m:r></m:e></m:fName><m:e><m:r><m:t>x</m:t></m:r></m:e></m:func>`)
	if got != "foobarx" {
		t.Errorf("doFunc mixed fName = %q, want foobarx", got)
	}
}
