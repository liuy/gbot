package media

import (
	"bytes"
	"strings"
)

// extToMime maps a lowercase file extension (with leading dot) to a MIME type.
// Ported verbatim from openclaw src/media/mime.ts:EXTENSION_TO_MIME so cache
// filenames for document attachments keep their original type.
var extToMime = map[string]string{
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

// mimeToExt maps a MIME type (lowercased, charset-stripped) to a file extension
// with leading dot. Ported verbatim from openclaw src/media/mime.ts:MIME_TO_EXTENSION.
var mimeToExt = map[string]string{
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

// imageMagicSignatures are the magic-byte prefixes for the four image formats
// the Anthropic API accepts (image/png, image/jpeg, image/gif, image/webp).
// WeChat image items frequently arrive without a useful filename, so the
// decrypted bytes are the only reliable type signal — hence the sniff.
var imageMagicSignatures = []struct {
	mime string
	head []byte
}{
	// PNG: 8-byte signature.
	{"image/png", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}},
	// JPEG: Start Of Image marker.
	{"image/jpeg", []byte{0xFF, 0xD8, 0xFF}},
	// GIF: "GIF8" (covers GIF87a and GIF89a).
	{"image/gif", []byte{0x47, 0x49, 0x46, 0x38}},
}

// ExtFromMime returns the file extension (with leading dot) for a MIME type,
// defaulting to ".bin". Port of openclaw src/media/mime.ts:getExtensionFromMime.
// The MIME type is split on ";" and trimmed/lowercased so "image/png; charset=..."
// collapses to "image/png".
func ExtFromMime(mimeType string) string {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	if ext, ok := mimeToExt[ct]; ok {
		return ext
	}
	return ".bin"
}

// MimeFromExt returns the MIME type for a file extension (with leading dot),
// defaulting to "application/octet-stream". Port of openclaw
// src/media/mime.ts:getMimeFromFilename. The extension is lowercased.
func MimeFromExt(ext string) string {
	if ext == "" {
		return "application/octet-stream"
	}
	if mime, ok := extToMime[strings.ToLower(ext)]; ok {
		return mime
	}
	return "application/octet-stream"
}

// SniffImageMime inspects the magic bytes of a buffer and returns the image
// MIME type ("image/png", "image/jpeg", "image/gif", "image/webp") or "" if the
// bytes are not a recognized image format. WebP needs a two-range check (RIFF
// at 0..3 and WEBP at 8..11), so it is handled separately from the prefix list.
func SniffImageMime(data []byte) string {
	for _, sig := range imageMagicSignatures {
		if bytes.HasPrefix(data, sig.head) {
			return sig.mime
		}
	}
	// WebP: "RIFF" .... "WEBP" — bytes 0-3 and 8-11.
	if len(data) >= 12 &&
		data[0] == 0x52 && data[1] == 0x49 && data[2] == 0x46 && data[3] == 0x46 &&
		data[8] == 0x57 && data[9] == 0x45 && data[10] == 0x42 && data[11] == 0x50 {
		return "image/webp"
	}
	return ""
}

// IsImageMime returns true for image/* MIME types gbot sends to the LLM.
func IsImageMime(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/")
}
