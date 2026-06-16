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
	"errors"
	"strings"
	"testing"
)

func TestUnsupportedFormatErrorEmpty(t *testing.T) {
	e := &UnsupportedFormatError{}
	got := e.Error()
	want := "unsupported format"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestUnsupportedFormatErrorWithExtension(t *testing.T) {
	e := &UnsupportedFormatError{Extension: ".xyz"}
	got := e.Error()
	if !strings.Contains(got, "unsupported format") {
		t.Errorf("Error() = %q, should contain 'unsupported format'", got)
	}
	if !strings.Contains(got, `extension=".xyz"`) {
		t.Errorf("Error() = %q, should contain extension=.xyz", got)
	}
}

func TestUnsupportedFormatErrorWithMIME(t *testing.T) {
	e := &UnsupportedFormatError{MIMEType: "application/foo"}
	got := e.Error()
	if !strings.Contains(got, "unsupported format") {
		t.Errorf("Error() = %q, should contain 'unsupported format'", got)
	}
	if !strings.Contains(got, `mime="application/foo"`) {
		t.Errorf("Error() = %q, should contain mime=application/foo", got)
	}
}

func TestUnsupportedFormatErrorBoth(t *testing.T) {
	e := &UnsupportedFormatError{Extension: ".xyz", MIMEType: "application/foo"}
	got := e.Error()
	if !strings.Contains(got, `extension=".xyz"`) {
		t.Errorf("Error() = %q, should contain extension", got)
	}
	if !strings.Contains(got, `mime="application/foo"`) {
		t.Errorf("Error() = %q, should contain mime", got)
	}
}

func TestConversionErrorEmpty(t *testing.T) {
	e := &ConversionError{}
	got := e.Error()
	want := "conversion failed"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestConversionErrorWithAttempts(t *testing.T) {
	e := &ConversionError{
		Attempts: []FailedConversionAttempt{
			{Converter: "csv", Err: errors.New("parse error")},
			{Converter: "html", Err: errors.New("read error")},
		},
	}
	got := e.Error()
	if !strings.Contains(got, "conversion failed after 2 attempt(s):") {
		t.Errorf("Error() = %q, should contain '2 attempt(s)'", got)
	}
	if !strings.Contains(got, "csv: parse error") {
		t.Errorf("Error() = %q, should contain csv attempt", got)
	}
	if !strings.Contains(got, "html: read error") {
		t.Errorf("Error() = %q, should contain html attempt", got)
	}
}

func TestConversionErrorUnwrapEmpty(t *testing.T) {
	e := &ConversionError{}
	if err := e.Unwrap(); err != nil {
		t.Errorf("Unwrap() on empty = %v, want nil", err)
	}
}

func TestConversionErrorUnwrapWithAttempts(t *testing.T) {
	lastErr := errors.New("the last error")
	e := &ConversionError{
		Attempts: []FailedConversionAttempt{
			{Converter: "csv", Err: errors.New("first error")},
			{Converter: "html", Err: lastErr},
		},
	}
	got := e.Unwrap()
	if got != lastErr {
		t.Errorf("Unwrap() = %v, want %v (last attempt)", got, lastErr)
	}
}

func TestIsUnsupportedFormatTrue(t *testing.T) {
	err := &UnsupportedFormatError{Extension: ".xyz"}
	if !IsUnsupportedFormat(err) {
		t.Errorf("IsUnsupportedFormat(UnsupportedFormatError) = false, want true")
	}
}

func TestIsUnsupportedFormatWrapped(t *testing.T) {
	inner := &UnsupportedFormatError{Extension: ".xyz"}
	wrapped := errors.Join(inner)
	if !IsUnsupportedFormat(wrapped) {
		t.Errorf("IsUnsupportedFormat(wrapped) = false, want true")
	}
}

func TestIsUnsupportedFormatFalse(t *testing.T) {
	err := errors.New("some other error")
	if IsUnsupportedFormat(err) {
		t.Errorf("IsUnsupportedFormat(other error) = true, want false")
	}
}

func TestIsUnsupportedFormatNil(t *testing.T) {
	if IsUnsupportedFormat(nil) {
		t.Errorf("IsUnsupportedFormat(nil) = true, want false")
	}
}
