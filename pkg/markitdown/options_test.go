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

import "testing"

func TestWithKeepDataURIs(t *testing.T) {
	m := &MarkItDown{}
	if m.keepDataURIs {
		t.Fatal("default keepDataURIs should be false")
	}
	opt := WithKeepDataURIs(true)
	opt(m)
	if !m.keepDataURIs {
		t.Errorf("keepDataURIs = false, want true after WithKeepDataURIs(true)")
	}

	opt = WithKeepDataURIs(false)
	opt(m)
	if m.keepDataURIs {
		t.Errorf("keepDataURIs = true, want false after WithKeepDataURIs(false)")
	}
}

func TestWithStyleMap(t *testing.T) {
	m := &MarkItDown{}
	if m.styleMap != "" {
		t.Fatalf("default styleMap should be empty, got %q", m.styleMap)
	}
	want := "custom-style-map-content"
	opt := WithStyleMap(want)
	opt(m)
	if m.styleMap != want {
		t.Errorf("styleMap = %q, want %q", m.styleMap, want)
	}
}

func TestNewWithKeepDataURIs(t *testing.T) {
	m := New(WithKeepDataURIs(true))
	if !m.keepDataURIs {
		t.Errorf("New(WithKeepDataURIs(true)): keepDataURIs = false, want true")
	}
}

func TestNewWithStyleMap(t *testing.T) {
	want := "mystyle"
	m := New(WithStyleMap(want))
	if m.styleMap != want {
		t.Errorf("New(WithStyleMap(%q)): styleMap = %q, want %q", want, m.styleMap, want)
	}
}

func TestNewWithBothOptions(t *testing.T) {
	m := New(WithKeepDataURIs(true), WithStyleMap("abc"))
	if !m.keepDataURIs {
		t.Errorf("keepDataURIs = false, want true")
	}
	if m.styleMap != "abc" {
		t.Errorf("styleMap = %q, want abc", m.styleMap)
	}
}

func TestNewDefaultNoOptions(t *testing.T) {
	m := New()
	if m.keepDataURIs {
		t.Errorf("default keepDataURIs = true, want false")
	}
	if m.styleMap != "" {
		t.Errorf("default styleMap = %q, want empty", m.styleMap)
	}
	if len(m.converters) == 0 {
		t.Errorf("expected builtin converters to be registered")
	}
}
