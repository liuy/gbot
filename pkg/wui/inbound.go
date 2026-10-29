package wui

// inboundContent is the wire shape of one entry in user_message.content[]
// (legacy) or one entry produced by resolveAttachments for the commit frame.
// Path is the absolute media.Store object the content-addressed id resolved to.
type inboundContent struct {
	Type   string        `json:"type"` // "image" | "document"
	Source inboundSource `json:"source"`
}

// inboundSource is the source payload of an inbound content block. Only
// "file" is supported — base64-inline images arrive through other channels
// (wechat downloadImage) and are not produced by the WUI frontend. Size is
// the commit-time declared byte count, carried so document reference blocks
// can show it without re-statting the file.
type inboundSource struct {
	Type string `json:"type"` // "file"
	Path string `json:"path"`
	Mime string `json:"mime,omitempty"`
	Name string `json:"name,omitempty"`
	Size int64  `json:"size,omitempty"`
}
