package wui

// inboundContent is the wire shape of one entry in user_message.content[]
// (legacy) or one entry produced by attachmentAccumulator.buildContents
// (new WS upload path). Path is the absolute media.Store path the
// accumulator assigned after bytes landed via binary frames.
type inboundContent struct {
	Type   string        `json:"type"` // "image" | "document"
	Source inboundSource `json:"source"`
}

// inboundSource is the source payload of an inbound content block. Only
// "file" is supported — base64-inline images arrive through other channels
// (wechat downloadImage) and are not produced by the WUI frontend.
type inboundSource struct {
	Type string `json:"type"` // "file"
	Path string `json:"path"`
	Mime string `json:"mime,omitempty"`
	Name string `json:"name,omitempty"`
}
