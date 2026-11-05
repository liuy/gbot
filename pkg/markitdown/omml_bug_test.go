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
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// The *Pr children of acc/bar/d/f/groupChr are optional in OMML; a missing one
// must render exactly like an empty <m:xPr/> element (parsePr's zero struct).
func TestConvertOMMLWithoutPrChild(t *testing.T) {
	cases := []struct {
		name  string
		inner string
		want  string
	}{
		{"acc without accPr", `<m:acc><m:e><m:r><m:t>x</m:t></m:r></m:e></m:acc>`, `\hat{{x}}`},
		{"bar without barPr", `<m:bar><m:e><m:r><m:t>x</m:t></m:r></m:e></m:bar>`, `\overline{{x}}`},
		{"d without dPr", `<m:d><m:e><m:r><m:t>x</m:t></m:r></m:e></m:d>`, `\left(x\right)`},
		{"f without fPr", `<m:f><m:num><m:r><m:t>1</m:t></m:r></m:num><m:den><m:r><m:t>2</m:t></m:r></m:den></m:f>`, `\frac{{1}}{{2}}`},
		{"groupChr without groupChrPr", `<m:groupChr><m:e><m:r><m:t>G</m:t></m:r></m:e></m:groupChr>`, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := convertSingle(t, tc.inner); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// End-to-end surface: a docx whose inline oMath carries a bare <m:acc> flows
// through preProcessMath into ConvertOMML (converter_docx.go Convert ->
// findOMath), the same chain real uploads hit.
func TestDocxConvertMathWithoutPrSucceeds(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"><w:body><w:p><m:oMath><m:acc><m:e><m:r><m:t>x</m:t></m:r></m:e></m:acc></m:oMath></w:p></w:body></w:document>`
	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
		"word/document.xml":   doc,
	}
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := NewDocxConverter(New()).Convert(bytes.NewReader(buf.Bytes()), StreamInfo{Extension: ".docx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	// The HTML-to-markdown tail of the chain escapes the backslash, so the
	// latex \hat{{x}} lands as \\hat{{x}} - same form every docx math takes.
	if !strings.Contains(result.Markdown, `$\\hat{{x}}$`) {
		t.Errorf("bare acc must render the default hat inline, got %q", result.Markdown)
	}
}
