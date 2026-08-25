package markitdown

import (
	"os"
	"testing"
)

// TestMain warms the PDFium WASM pool once for the whole package. The pool
// init costs ~24s under the race detector (WASM compile + instantiate);
// without this, whichever PDF test runs first pays the entire cost, and
// with per-test timings polluted it also obscures real converter behavior.
// sync.Once inside the converter makes the second Init call a no-op.
func TestMain(m *testing.M) {
	c := NewPdfConverter()
	f, err := os.Open("testdata/test.pdf")
	if err == nil {
		_, _ = c.Convert(f, StreamInfo{Extension: ".pdf", MIMEType: "application/pdf"})
		f.Close()
	}
	os.Exit(m.Run())
}
