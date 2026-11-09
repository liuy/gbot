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

// ---------------------------------------------------------------------------
// Map tables and sniff boundaries — every entry is pinned
// ---------------------------------------------------------------------------

func TestExtToMime_Table(t *testing.T) {
	want := map[string]string{
		".pdf":  "application/pdf",
		".doc":  "application/msword",
		".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		".xls":  "application/vnd.ms-excel",
		".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		".ppt":  "application/vnd.ms-powerpoint",
		".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		".txt":  "text/plain",
		".csv":  "text/csv",
		".zip":  "application/zip",
		".tar":  "application/x-tar",
		".gz":   "application/gzip",
		".mp3":  "audio/mpeg",
		".ogg":  "audio/ogg",
		".wav":  "audio/wav",
		".mp4":  "video/mp4",
		".mov":  "video/quicktime",
		".webm": "video/webm",
		".mkv":  "video/x-matroska",
		".avi":  "video/x-msvideo",
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".gif":  "image/gif",
		".webp": "image/webp",
		".bmp":  "image/bmp",
	}
	if len(extToMime) != len(want) {
		t.Errorf("extToMime has %d entries, want %d", len(extToMime), len(want))
	}
	for ext, mime := range want {
		if got := MimeFromExt(ext); got != mime {
			t.Errorf("MimeFromExt(%q) = %q, want %q", ext, got, mime)
		}
	}
	if got := MimeFromExt(".unknown"); got != "application/octet-stream" {
		t.Errorf("MimeFromExt(.unknown) = %q, want application/octet-stream", got)
	}
	if got := MimeFromExt(""); got != "application/octet-stream" {
		t.Errorf("MimeFromExt(\"\") = %q, want application/octet-stream", got)
	}
	if got := MimeFromExt(".PNG"); got != "image/png" {
		t.Errorf("MimeFromExt(.PNG) = %q, want image/png (case-insensitive)", got)
	}
}

func TestMimeToExt_Table(t *testing.T) {
	want := map[string]string{
		"image/jpeg":        ".jpg",
		"image/jpg":         ".jpg",
		"image/png":         ".png",
		"image/gif":         ".gif",
		"image/webp":        ".webp",
		"image/bmp":         ".bmp",
		"video/mp4":         ".mp4",
		"video/quicktime":   ".mov",
		"video/webm":        ".webm",
		"video/x-matroska":  ".mkv",
		"video/x-msvideo":   ".avi",
		"audio/mpeg":        ".mp3",
		"audio/ogg":         ".ogg",
		"audio/wav":         ".wav",
		"application/pdf":   ".pdf",
		"application/zip":   ".zip",
		"application/x-tar": ".tar",
		"application/gzip":  ".gz",
		"text/plain":        ".txt",
		"text/csv":          ".csv",
	}
	if len(mimeToExt) != len(want) {
		t.Errorf("mimeToExt has %d entries, want %d", len(mimeToExt), len(want))
	}
	for mime, ext := range want {
		if got := ExtFromMime(mime); got != ext {
			t.Errorf("ExtFromMime(%q) = %q, want %q", mime, got, ext)
		}
	}
	if got := ExtFromMime("image/png; charset=binary"); got != ".png" {
		t.Errorf("ExtFromMime with a charset parameter = %q, want .png", got)
	}
	if got := ExtFromMime("application/unknown"); got != ".bin" {
		t.Errorf("ExtFromMime(unknown) = %q, want .bin", got)
	}
}

func TestSniffImageMime_WebPRequiresEveryMagicByte(t *testing.T) {
	valid := []byte("RIFF\x01\x02\x03\x04WEBP")
	if got := SniffImageMime(valid); got != "image/webp" {
		t.Fatalf("SniffImageMime(valid) = %q, want image/webp", got)
	}
	for i := range 12 {
		if i == 4 || i == 5 || i == 6 || i == 7 {
			continue // the RIFF size field is not part of the signature
		}
		bad := append([]byte(nil), valid...)
		bad[i] ^= 0xFF
		if got := SniffImageMime(bad); got != "" {
			t.Errorf("byte %d flipped: SniffImageMime = %q, want \"\"", i, got)
		}
	}
	if got := SniffImageMime([]byte("RIFF\x01\x02\x03\x04WEP")); got != "" {
		t.Errorf("11-byte RIFF prefix = %q, want \"\"", got)
	}
	if got := SniffImageMime([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}); got != "image/png" {
		t.Errorf("PNG signature = %q, want image/png", got)
	}
	if got := SniffImageMime([]byte{0xFF, 0xD8, 0xFF}); got != "image/jpeg" {
		t.Errorf("JPEG SOI = %q, want image/jpeg", got)
	}
	if got := SniffImageMime([]byte("GIF89a")); got != "image/gif" {
		t.Errorf("GIF89a = %q, want image/gif", got)
	}
	if got := SniffImageMime(nil); got != "" {
		t.Errorf("empty buffer = %q, want \"\"", got)
	}
}
