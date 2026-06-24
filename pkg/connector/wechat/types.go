// Package wechat implements the WeChat connector using Tencent's iLink Bot API.
package wechat

import (
	"encoding/json"
	"errors"
	"fmt"
)

// iLink API endpoints
const (
	EPGetUpdates   = "ilink/bot/getupdates"
	EPSendMessage  = "ilink/bot/sendmessage"
	EPSendTyping   = "ilink/bot/sendtyping"
	EPGetConfig    = "ilink/bot/getconfig"
	EPGetUploadURL = "ilink/bot/getuploadurl"
	EPGetBotQR     = "ilink/bot/get_bot_qrcode"
	EPGetQRStatus  = "ilink/bot/get_qrcode_status"

	ILINKBaseURL   = "https://ilinkai.weixin.qq.com"
	ILINKAppID     = "bot"
	ChannelVersion = "2.2.0"
	AppClientVer   = (2 << 16) | (2 << 8)
)

// iLink message item types
const (
	ItemText  = 1
	ItemImage = 2
	ItemVoice = 3
	ItemFile  = 4
	ItemVideo = 5
)

// iLink message types and states
const (
	MsgTypeUser    = 1
	MsgTypeBot     = 2
	MsgStateFinish = 2
)

// Media types for upload
const (
	MediaImage = 1
	MediaVideo = 2
	MediaFile  = 3
	MediaVoice = 4
)

// Typing status
const (
	TypingStart = 1
	TypingStop  = 2
)

// Error codes
const (
	SessionExpiredErrCode = -14
	RateLimitErrCode      = -2
)

// Timeouts (milliseconds)
const (
	LongPollTimeoutMs = 35000
	APITimeoutMs      = 15000
	ConfigTimeoutMs   = 10000
	QRTimeoutMs       = 35000
)

// Retry constants
const (
	MaxConsecutiveFailures = 3
	RetryDelaySeconds      = 2
	BackoffDelaySeconds    = 30
	MessageDedupTTLSeconds = 300
)

// Sentinel errors
var (
	ErrSessionExpired = errors.New("iLink session expired")
	ErrRateLimited    = errors.New("iLink rate limited")
)

// FlexString accepts both JSON string and number, converting to string.
type FlexString string

func (f *FlexString) UnmarshalJSON(data []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = FlexString(s)
		return nil
	}
	// Try number
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*f = FlexString(n.String())
		return nil
	}
	return fmt.Errorf("FlexString: cannot unmarshal %s", string(data))
}

// Message is a single iLink message from getupdates response.
type Message struct {
	FromUserID   string     `json:"from_user_id"`
	ToUserID     string     `json:"to_user_id"`
	MessageID    FlexString `json:"message_id"`
	ContextToken string     `json:"context_token"`
	ClientID     string     `json:"client_id"`
	MsgType      int        `json:"message_type"`
	MsgState     int        `json:"message_state"`
	ItemList     []Item     `json:"item_list"`
	RoomID       string     `json:"room_id,omitempty"`
	ChatRoomID   string     `json:"chat_room_id,omitempty"`
}

// Item is a single content item within a message.
type Item struct {
	Type      int              `json:"type"`
	TextItem  *TextItem        `json:"text_item,omitempty"`
	ImageItem *MediaItemHolder `json:"image_item,omitempty"`
	VoiceItem *VoiceItem       `json:"voice_item,omitempty"`
	FileItem  *FileItem        `json:"file_item,omitempty"`
	VideoItem *MediaItemHolder `json:"video_item,omitempty"`
	RefMsg    *RefMsg          `json:"ref_msg,omitempty"`
}

// TextItem holds text content.
type TextItem struct {
	Text string `json:"text"`
}

// MediaItemHolder wraps a media reference for image/video items.
type MediaItemHolder struct {
	Media *MediaRef `json:"media,omitempty"`
}

// VoiceItem holds voice media with optional transcribed text.
type VoiceItem struct {
	Media *MediaRef `json:"media,omitempty"`
	Text  string    `json:"text,omitempty"`
}

// MediaRef references encrypted media on the WeChat CDN.
type MediaRef struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AesKey            string `json:"aes_key,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
}

// FileItem holds file media with metadata.
type FileItem struct {
	Media    *MediaRef `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	Len      string    `json:"len,omitempty"`
}

// RefMsg holds a referenced/forwarded message.
type RefMsg struct {
	Title       string `json:"title,omitempty"`
	MessageItem *Item  `json:"message_item,omitempty"`
}

// GetUpdatesResponse is the response from getupdates.
type GetUpdatesResponse struct {
	Ret           int       `json:"ret"`
	ErrCode       int       `json:"errcode"`
	ErrMsg        string    `json:"errmsg"`
	Msgs          []Message `json:"msgs"`
	GetUpdatesBuf string    `json:"get_updates_buf"`
	LongPollingMs int       `json:"longpolling_timeout_ms,omitempty"`
}

// GetUpdatesRequest is the request body for getupdates.
type GetUpdatesRequest struct {
	GetUpdatesBuf string    `json:"get_updates_buf"`
	BaseInfo      *BaseInfo `json:"base_info"`
}

// BaseInfo is included in every iLink request.
type BaseInfo struct {
	ChannelVersion string `json:"channel_version"`
}

// SendMessageRequest is the request body for sendmessage.
type SendMessageRequest struct {
	Msg      *OutboundMessage `json:"msg"`
	BaseInfo *BaseInfo        `json:"base_info"`
}

// OutboundMessage is a message sent through the iLink API.
type OutboundMessage struct {
	FromUserID   string `json:"from_user_id"`
	ToUserID     string `json:"to_user_id"`
	ClientID     string `json:"client_id"`
	MessageType  int    `json:"message_type"`
	MessageState int    `json:"message_state"`
	ItemList     []Item `json:"item_list"`
	ContextToken string `json:"context_token,omitempty"`
}

// GetConfigResponse is the response from getconfig.
type GetConfigResponse struct {
	TypingTicket string `json:"typing_ticket"`
}

// GetBotQRResponse is the response from get_bot_qrcode.
type GetBotQRResponse struct {
	QRCode    string `json:"qrcode"`
	QRCodeImg string `json:"qrcode_img_content"`
}

// GetQRStatusResponse is the response from get_qrcode_status.
type GetQRStatusResponse struct {
	Status       string `json:"status"` // "wait", "scaned", "confirmed", "expired"
	RedirectHost string `json:"redirect_host,omitempty"`
	ILinkBotID   string `json:"ilink_bot_id,omitempty"`
	BotToken     string `json:"bot_token,omitempty"`
	BaseURL      string `json:"baseurl,omitempty"`
	ILinkUserID  string `json:"ilink_user_id,omitempty"`
}

// State is the on-disk state structure for ~/.gbot/wechat/state.json.
type State struct {
	AccountID     string            `json:"account_id"`
	Token         string            `json:"token"`
	BaseURL       string            `json:"base_url"`
	SyncBuf       string            `json:"sync_buf"`
	ContextTokens map[string]string `json:"context_tokens,omitempty"`
	EngineID      string            `json:"engine_id,omitempty"`
}
