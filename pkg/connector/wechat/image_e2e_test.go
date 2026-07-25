package wechat

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// TestImagePipeline_ProducesBase64BlockUnderAPILimit exercises the full
// wechat image path: CDN download → AES decrypt → cache save → fileread
// resize/encode → ContentBlock dispatched to QueryWithContent. Asserts the
// final block is base64 (not file), carries decoded image bytes, and stays
// under the LLM API's 5MB image payload limit.
func TestImagePipeline_ProducesBase64BlockUnderAPILimit(t *testing.T) {
	t.Parallel()

	// Real PNG bytes so image.Decode succeeds inside fileread.executeImage.
	plaintext := realPNGBytes
	key := []byte("0123456789abcdef")
	ciphertext := encryptAesEcbForMediaTest(plaintext, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	msg := Message{
		FromUserID: "user1",
		MessageID:  FlexString("msg-e2e"),
		ItemList: []Item{
			{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{
				FullURL: srv.URL, AesKey: aesKeyB64,
			}}},
		},
	}
	c.processBatch(context.Background(), []Message{msg})

	select {
	case im := <-c.inboundCh:
		// Image block + default prompt — both must reach QueryWithContent.
		if len(im.content) < 1 {
			t.Fatalf("im.content length = %d, want >= 1", len(im.content))
		}
		imgBlock := im.content[0]
		if imgBlock.Type != types.ContentTypeImage {
			t.Fatalf("im.content[0].Type = %q, want image", imgBlock.Type)
		}
		if imgBlock.Source == nil {
			t.Fatal("imgBlock.Source = nil")
		}
		if imgBlock.Source.Type != "base64" {
			t.Errorf("Source.Type = %q, want base64", imgBlock.Source.Type)
		}
		decoded, err := base64.StdEncoding.DecodeString(imgBlock.Source.Data)
		if err != nil {
			t.Fatalf("base64 decode: %v", err)
		}
		if len(decoded) == 0 {
			t.Error("decoded payload is empty")
		}
		// 5MB matches pkg/utils.API_IMAGE_MAX_BASE64_SIZE — the LLM API limit
		// applies to the base64 string length, not the decoded bytes. fileread
		// is responsible for enforcing via MaybeResizeAndDownsampleImageBuffer.
		if len(imgBlock.Source.Data) > 5*1024*1024 {
			t.Errorf("base64 payload = %d bytes, must be <= 5MB", len(imgBlock.Source.Data))
		}
		if !strings.HasPrefix(imgBlock.Source.MediaType, "image/") {
			t.Errorf("Source.MediaType = %q, want image/* prefix", imgBlock.Source.MediaType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processBatch did not enqueue within 2s")
	}
}
