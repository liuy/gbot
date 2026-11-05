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
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestIpynbConstructorReturnsInstance(t *testing.T) {
	if NewIpynbConverter() == nil {
		t.Fatal("NewIpynbConverter() = nil, want non-nil")
	}
}

func TestIpynbConvertReadErrorPreservesCause(t *testing.T) {
	c := NewIpynbConverter()
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".ipynb"})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Convert read error must wrap the reader error via %%w, got: %v", err)
	}
}

func TestIpynbConvertParseErrorPreservesCause(t *testing.T) {
	c := NewIpynbConverter()
	_, err := c.Convert(strings.NewReader("not json"), StreamInfo{Extension: ".ipynb"})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("parse error must wrap json.SyntaxError via %%w, got: %v", err)
	}
}

// An empty kernelspec language must fall back to python rather than emit an
// unlabeled code fence.
func TestIpynbEmptyKernelLanguageFallsBackToPython(t *testing.T) {
	nb := `{"metadata":{"kernelspec":{"language":""}},"cells":[{"cell_type":"code","source":"x = 1"}]}`
	c := NewIpynbConverter()
	result, err := c.Convert(strings.NewReader(nb), StreamInfo{Extension: ".ipynb"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "```python\n") {
		t.Errorf("Markdown = %q, want python fence", result.Markdown)
	}
}

// The title is the first heading of the first markdown cell: later cells and
// later lines must not overwrite it.
func TestIpynbTitleFromFirstHeadingOnly(t *testing.T) {
	nb := `{"cells":[
		{"cell_type":"markdown","source":"# First Title\n\nbody"},
		{"cell_type":"markdown","source":"# Second Title"}
	]}`
	c := NewIpynbConverter()
	result, err := c.Convert(strings.NewReader(nb), StreamInfo{Extension: ".ipynb"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if result.Title != "First Title" {
		t.Errorf("Title = %q, want %q", result.Title, "First Title")
	}
}

func TestIpynbTitleFromFirstLineOfCell(t *testing.T) {
	nb := `{"cells":[{"cell_type":"markdown","source":"# Head One\n# Head Two"}]}`
	c := NewIpynbConverter()
	result, err := c.Convert(strings.NewReader(nb), StreamInfo{Extension: ".ipynb"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if result.Title != "Head One" {
		t.Errorf("Title = %q, want %q", result.Title, "Head One")
	}
}

// The heading scan trims each line first: an indented "# " still counts.
func TestIpynbTitleTrimsLineBeforeMatch(t *testing.T) {
	nb := `{"cells":[{"cell_type":"markdown","source":"  # Indented Title"}]}`
	c := NewIpynbConverter()
	result, err := c.Convert(strings.NewReader(nb), StreamInfo{Extension: ".ipynb"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if result.Title != "Indented Title" {
		t.Errorf("Title = %q, want %q", result.Title, "Indented Title")
	}
}
