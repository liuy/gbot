package media

import (
	"testing"
)

func TestExtFromMime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mime string
		want string
	}{
		{"png", "image/png", ".png"},
		{"jpeg", "image/jpeg", ".jpg"},
		{"jpg alias", "image/jpg", ".jpg"},
		{"gif", "image/gif", ".gif"},
		{"webp", "image/webp", ".webp"},
		{"pdf", "application/pdf", ".pdf"},
		{"csv", "text/csv", ".csv"},
		{"with charset", "image/png; charset=utf-8", ".png"},
		{"uppercase stripped", "IMAGE/PNG", ".png"},
		{"unknown defaults bin", "application/x-custom", ".bin"},
		{"empty defaults bin", "", ".bin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ExtFromMime(tc.mime); got != tc.want {
				t.Errorf("ExtFromMime(%q) = %q, want %q", tc.mime, got, tc.want)
			}
		})
	}
}

func TestMimeFromExt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ext  string
		want string
	}{
		{"png", ".png", "image/png"},
		{"jpg", ".jpg", "image/jpeg"},
		{"jpeg", ".jpeg", "image/jpeg"},
		{"pdf", ".pdf", "application/pdf"},
		{"uppercase lowercased", ".PDF", "application/pdf"},
		{"xlsx", ".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"unknown defaults octet-stream", ".xyz", "application/octet-stream"},
		{"empty defaults octet-stream", "", "application/octet-stream"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MimeFromExt(tc.ext); got != tc.want {
				t.Errorf("MimeFromExt(%q) = %q, want %q", tc.ext, got, tc.want)
			}
		})
	}
}

func TestMimeExtRoundTrip(t *testing.T) {
	t.Parallel()
	// Known MIME types must round-trip through ExtFromMime and MimeFromExt.
	known := []string{".png", ".jpg", ".gif", ".webp", ".pdf", ".csv", ".txt"}
	for _, ext := range known {
		mime := MimeFromExt(ext)
		back := ExtFromMime(mime)
		// .jpeg and .jpg both map to image/jpeg → .jpg, so accept the canonical form.
		if ext == ".jpeg" {
			if back != ".jpg" {
				t.Errorf("round-trip .jpeg → %q → %q, want .jpg", mime, back)
			}
			continue
		}
		if back != ext {
			t.Errorf("round-trip %q → %q → %q, want %q", ext, mime, back, ext)
		}
	}
}

func TestSniffImageMime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			"png",
			[]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0},
			"image/png",
		},
		{
			"jpeg",
			[]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0},
			"image/jpeg",
		},
		{
			"gif87a",
			[]byte("GIF87a"),
			"image/gif",
		},
		{
			"gif89a",
			[]byte("GIF89a"),
			"image/gif",
		},
		{
			"webp",
			[]byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'},
			"image/webp",
		},
		{
			"unknown bytes",
			[]byte{0xDE, 0xAD, 0xBE, 0xEF},
			"",
		},
		{
			"empty",
			nil,
			"",
		},
		{
			"too short for webp",
			[]byte{'R', 'I', 'F', 'F', 0, 0, 0, 0},
			"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SniffImageMime(tc.data); got != tc.want {
				t.Errorf("SniffImageMime = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSniffImageMime_OnlyPngPrefix(t *testing.T) {
	t.Parallel()
	// Exactly the 8-byte PNG signature, no trailing bytes.
	if got := SniffImageMime([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}); got != "image/png" {
		t.Errorf("SniffImageMime exact PNG = %q, want image/png", got)
	}
}

func TestIsImageMime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mime string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/webp", true},
		{"application/pdf", false},
		{"text/plain", false},
		{"", false},
		{"image/", true}, // prefix match; degenerate but consistent
	}
	for _, tc := range tests {
		if got := IsImageMime(tc.mime); got != tc.want {
			t.Errorf("IsImageMime(%q) = %v, want %v", tc.mime, got, tc.want)
		}
	}
}
