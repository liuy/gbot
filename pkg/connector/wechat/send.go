package wechat

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/tool"
	sendtool "github.com/liuy/gbot/pkg/tool/send"
)

// SendFile uploads a local file to the CDN and sends it to the active user.
// Routes by extension: image → MediaImage, video → MediaVideo, else MediaFile.
// Port of openclaw src/messaging/send-media.ts:sendWeixinMediaFile.
//
// If caption is non-empty it is sent as a separate preceding TEXT item (one
// request per item, matching send.ts:sendMediaItems), then the media item.
func (c *WeChatConnector) SendFile(ctx context.Context, filePath, caption string) error {
	if c.activeUserID == "" {
		return fmt.Errorf("send: no active user")
	}

	mediaType := mediaTypeFromExt(filepath.Ext(filePath))

	uploaded, err := c.uploadFn(ctx, c.client, c.state.BaseURL, c.state.Token,
		c.activeUserID, filePath, mediaType)
	if err != nil {
		return fmt.Errorf("send: upload: %w", err)
	}

	// TS does Buffer.from(uploaded.aeskey).toString("base64") where aeskey is
	// the hex STRING — Buffer.from(string) with no encoding treats it as UTF-8
	// bytes. So we base64-encode the UTF-8 bytes of the hex string, NOT the
	// hex-decoded raw bytes. The WeChat client decodes this back symmetrically.
	aesKeyB64 := base64.StdEncoding.EncodeToString([]byte(uploaded.AesKey))
	mediaRef := &MediaRef{
		EncryptQueryParam: uploaded.DownloadEncryptedQueryParam,
		AesKey:            aesKeyB64,
		EncryptType:       1,
	}

	var item Item
	switch mediaType {
	case MediaImage:
		item = Item{Type: ItemImage, ImageItem: &MediaItemHolder{
			Media:   mediaRef,
			MidSize: uploaded.FileSizeCiphertext,
		}}
	case MediaVideo:
		item = Item{Type: ItemVideo, VideoItem: &MediaItemHolder{
			Media:     mediaRef,
			VideoSize: uploaded.FileSizeCiphertext,
		}}
	default:
		item = Item{Type: ItemFile, FileItem: &FileItem{
			Media:    mediaRef,
			FileName: filepath.Base(filePath),
			Len:      strconv.Itoa(uploaded.FileSize),
		}}
	}

	contextToken := c.getContextToken(c.activeUserID)

	// Caption goes out first as its own TEXT item (send.ts:139-188 loops one
	// item per request), then the media item in its own request.
	if caption != "" {
		textItem := Item{Type: ItemText, TextItem: &TextItem{Text: caption}}
		if err := c.sendItemFn(ctx, c.client, c.state.BaseURL, c.state.Token,
			c.state.AccountID, c.activeUserID, textItem, contextToken, ""); err != nil {
			return fmt.Errorf("send: caption: %w", err)
		}
	}

	if err := c.sendItemFn(ctx, c.client, c.state.BaseURL, c.state.Token,
		c.state.AccountID, c.activeUserID, item, contextToken, ""); err != nil {
		return fmt.Errorf("send: media: %w", err)
	}
	return nil
}

// mediaTypeFromExt routes a file extension to the iLink media type constant,
// matching send-media.ts:getMimeFromFilename + the video/image prefix checks.
func mediaTypeFromExt(ext string) int {
	mime := media.MimeFromExt(ext)
	if strings.HasPrefix(mime, "video/") {
		return MediaVideo
	}
	if strings.HasPrefix(mime, "image/") {
		return MediaImage
	}
	return MediaFile
}

// RegisterSendTool adds the Send tool to the given tool registry. Called from
// main.go after the connector exists. Idempotent: a no-op if a "Send" tool is
// already registered.
func (c *WeChatConnector) RegisterSendTool(reg *tool.Registry) {
	if _, ok := reg.Lookup("Send"); ok {
		return
	}
	reg.MustRegister(sendtool.New(c))
}
