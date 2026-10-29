package wui

import (
	"testing"
)

// minimalPNGAttachment is a real, decodable 1x1 PNG used as the image body by
// the upload tests (handler-level and e2e).
var minimalPNGAttachment = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xDE,
	0x00, 0x00, 0x00, 0x10, 0x49, 0x44, 0x41, 0x54,
	0x78, 0x9C, 0x62, 0xFA, 0xCF, 0xC0, 0x00, 0x08,
	0x00, 0x00, 0xFF, 0xFF, 0x03, 0x09, 0x01, 0x02,
	0x58, 0xB6, 0xD5, 0x50,
	0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44,
	0xAE, 0x42, 0x60, 0x82,
}

// pdfFixture is a minimal-but-realistic PDF body — enough that the document
// classification path (magic-byte sniff fails, filename ext drives the mime)
// yields application/pdf with a .pdf cache name.
var pdfFixture = []byte("%PDF-1.4 fake pdf content")

// TestClassifyAttachmentDocumentValues pins the document branch of
// classifyAttachment: real mime/ext/kind values derived from the filename
// extension when the bytes are not an image.
func TestClassifyAttachmentDocumentValues(t *testing.T) {
	mime, ext, kind := classifyAttachment([]byte("not an image"), "report.pdf", "text/plain")
	if mime != "application/pdf" {
		t.Errorf("mime = %q, want application/pdf", mime)
	}
	if ext != ".pdf" {
		t.Errorf("ext = %q, want .pdf (filename extension preserved)", ext)
	}
	if kind != "document" {
		t.Errorf("kind = %q, want document", kind)
	}
	if got := preserveDocExt("archive"); got != ".bin" {
		t.Errorf("preserveDocExt(no extension) = %q, want .bin", got)
	}
}
