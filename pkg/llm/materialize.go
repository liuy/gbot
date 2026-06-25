package llm

import (
	"encoding/base64"
	"fmt"
	"os"

	"github.com/liuy/gbot/pkg/types"
)

// MaterializeFileImages walks req.Messages and, for every image content block
// whose Source.Type == "file", reads the file at Source.Path and replaces the
// block in place with a base64 source (Source.Type = "base64", Source.Data =
// base64-encoded bytes, MediaType preserved, Path cleared). Blocks already
// base64, and non-image blocks, are untouched.
//
// On error the request may be partially mutated (blocks 0..k-1 already
// converted). Callers must not retry the same *Request after an error.
//
// This runs inside Complete/Stream AFTER the request is built but BEFORE
// json.Marshal, so the on-wire payload only ever contains base64 — the wire
// formats the Anthropic/OpenAI APIs accept have no native file-path source.
// Materialization happens before ValidateImagesForAPI so the base64 length
// check sees the real payload size.
func MaterializeFileImages(req *Request) error {
	for i := range req.Messages {
		for j := range req.Messages[i].Content {
			cb := &req.Messages[i].Content[j]
			if cb.Type != types.ContentTypeImage || cb.Source == nil || !cb.Source.IsFileSource() {
				continue
			}
			raw, err := os.ReadFile(cb.Source.Path)
			if err != nil {
				return fmt.Errorf("materialize image %s: %w", cb.Source.Path, err)
			}
			cb.Source.Type = "base64"
			cb.Source.Data = base64.StdEncoding.EncodeToString(raw)
			cb.Source.Path = ""
		}
	}
	return nil
}
