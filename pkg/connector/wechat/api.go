package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// baseInfo returns the base_info payload common to all requests.
func baseInfo() *BaseInfo {
	return &BaseInfo{ChannelVersion: ChannelVersion}
}

// randomWechatUIN generates a random X-WECHAT-UIN header value.
func randomWechatUIN() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	value := binary.BigEndian.Uint32(buf[:])
	return base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%d", value))
}

// buildHeaders builds HTTP headers for iLink API requests.
// token may be empty for QR endpoints.
func buildHeaders(token, body string) map[string]string {
	headers := map[string]string{
		"Content-Type":            "application/json",
		"AuthorizationType":       "ilink_bot_token",
		"Content-Length":          fmt.Sprintf("%d", len(body)),
		"X-WECHAT-UIN":            randomWechatUIN(),
		"iLink-App-Id":            ILINKAppID,
		"iLink-App-ClientVersion": fmt.Sprintf("%d", AppClientVer),
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}

// apiPost sends a POST request to the iLink API and decodes the JSON response.
func apiPost(ctx context.Context, client *http.Client, baseURL, endpoint string,
	payload any, token string, timeout time.Duration) (json.RawMessage, error) {

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("iLink %s marshal: %w", endpoint, err)
	}

	url := strings.TrimRight(baseURL, "/") + "/" + endpoint
	headers := buildHeaders(token, string(bodyBytes))

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("iLink %s request: %w", endpoint, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Apply timeout via context
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iLink %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("iLink %s read: %w", endpoint, err)
	}

	if resp.StatusCode != http.StatusOK {
		preview := string(raw)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("iLink %s: HTTP %d: %s", endpoint, resp.StatusCode, preview)
	}

	return json.RawMessage(raw), nil
}

// apiGet sends a GET request (used for QR endpoints).
func apiGet(ctx context.Context, client *http.Client, baseURL, endpoint string,
	timeout time.Duration) (json.RawMessage, error) {

	url := strings.TrimRight(baseURL, "/") + "/" + endpoint

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("iLink GET %s request: %w", endpoint, err)
	}
	req.Header.Set("iLink-App-Id", ILINKAppID)
	req.Header.Set("iLink-App-ClientVersion", fmt.Sprintf("%d", AppClientVer))

	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iLink GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("iLink GET %s read: %w", endpoint, err)
	}

	if resp.StatusCode != http.StatusOK {
		preview := string(raw)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("iLink GET %s: HTTP %d: %s", endpoint, resp.StatusCode, preview)
	}

	return json.RawMessage(raw), nil
}

// decodeResponse decodes a JSON raw message into the target type.
func decodeResponse(raw json.RawMessage, v any) error {
	return json.Unmarshal(raw, v)
}

// GetUpdates long-polls for new messages.
func GetUpdates(ctx context.Context, client *http.Client, baseURL, token, syncBuf string,
	timeout time.Duration) (*GetUpdatesResponse, error) {

	payload := map[string]any{
		"get_updates_buf": syncBuf,
		"base_info":       baseInfo(),
	}

	raw, err := apiPost(ctx, client, baseURL, EPGetUpdates, payload, token, timeout)
	if err != nil {
		// Check for context deadline exceeded (timeout) - not an error
		if ctx.Err() == context.DeadlineExceeded {
			return &GetUpdatesResponse{
				Ret:           0,
				Msgs:          nil,
				GetUpdatesBuf: syncBuf,
			}, nil
		}
		return nil, err
	}

	var resp GetUpdatesResponse
	if err := decodeResponse(raw, &resp); err != nil {
		return nil, fmt.Errorf("iLink getupdates decode: %w", err)
	}
	return &resp, nil
}

// SendMessage sends a text message to a WeChat user.
func SendMessage(ctx context.Context, client *http.Client, baseURL, token,
	fromUser, toUser, text, contextToken, clientID string) error {

	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("_send_message: text must not be empty")
	}

	if clientID == "" {
		clientID = fmt.Sprintf("gbot-wechat-%d", time.Now().UnixNano())
	}

	msg := OutboundMessage{
		FromUserID:   fromUser,
		ToUserID:     toUser,
		ClientID:     clientID,
		MessageType:  MsgTypeBot,
		MessageState: MsgStateFinish,
		ItemList: []Item{
			{
				Type:     ItemText,
				TextItem: &TextItem{Text: text},
			},
		},
	}
	if contextToken != "" {
		msg.ContextToken = contextToken
	}

	payload := SendMessageRequest{
		Msg:      &msg,
		BaseInfo: baseInfo(),
	}

	raw, err := apiPost(ctx, client, baseURL, EPSendMessage, payload, token, APITimeoutMs*time.Millisecond)
	if err != nil {
		return err
	}

	// Check response for error codes
	var resp struct {
		Ret     int    `json:"ret"`
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := decodeResponse(raw, &resp); err != nil {
		return fmt.Errorf("iLink sendmessage decode: %w", err)
	}

	return checkSendMessageResponse(resp.Ret, resp.ErrCode, resp.ErrMsg)
}

// checkSendMessageResponse checks the response error codes and returns typed errors.
func checkSendMessageResponse(ret, errcode int, errmsg string) error {
	if ret == 0 && errcode == 0 {
		return nil
	}
	if ret == SessionExpiredErrCode || errcode == SessionExpiredErrCode {
		return ErrSessionExpired
	}
	if (ret == RateLimitErrCode || errcode == RateLimitErrCode) &&
		strings.ToLower(errmsg) == "unknown error" {
		return ErrSessionExpired
	}
	if ret == RateLimitErrCode || errcode == RateLimitErrCode {
		return ErrRateLimited
	}
	return fmt.Errorf("iLink sendmessage: ret=%d errcode=%d errmsg=%s", ret, errcode, errmsg)
}

// SendTyping sends a typing indicator.
func SendTyping(ctx context.Context, client *http.Client, baseURL, token,
	userID, typingTicket string, status int) error {

	payload := map[string]any{
		"ilink_user_id": userID,
		"typing_ticket": typingTicket,
		"status":        status,
		"base_info":     baseInfo(),
	}

	_, err := apiPost(ctx, client, baseURL, EPSendTyping, payload, token, ConfigTimeoutMs*time.Millisecond)
	return err
}

// GetConfig fetches config (typing ticket) for a user.
func GetConfig(ctx context.Context, client *http.Client, baseURL, token,
	userID, contextToken string) (*GetConfigResponse, error) {

	payload := map[string]any{
		"ilink_user_id": userID,
		"base_info":     baseInfo(),
	}
	if contextToken != "" {
		payload["context_token"] = contextToken
	}

	raw, err := apiPost(ctx, client, baseURL, EPGetConfig, payload, token, ConfigTimeoutMs*time.Millisecond)
	if err != nil {
		return nil, err
	}

	var resp GetConfigResponse
	if err := decodeResponse(raw, &resp); err != nil {
		return nil, fmt.Errorf("iLink getconfig decode: %w", err)
	}
	return &resp, nil
}

// GetBotQR fetches the QR code for login.
func GetBotQR(ctx context.Context, client *http.Client,
	botType string) (*GetBotQRResponse, error) {

	raw, err := apiGet(ctx, client, ILINKBaseURL,
		EPGetBotQR+"?bot_type="+botType, QRTimeoutMs*time.Millisecond)
	if err != nil {
		return nil, err
	}

	var resp GetBotQRResponse
	if err := decodeResponse(raw, &resp); err != nil {
		return nil, fmt.Errorf("iLink getbotqr decode: %w", err)
	}
	return &resp, nil
}

// GetQRStatus polls the QR code login status.
func GetQRStatus(ctx context.Context, client *http.Client,
	baseURL, qrcode string) (*GetQRStatusResponse, error) {

	raw, err := apiGet(ctx, client, baseURL,
		EPGetQRStatus+"?qrcode="+qrcode, QRTimeoutMs*time.Millisecond)
	if err != nil {
		return nil, err
	}

	var resp GetQRStatusResponse
	if err := decodeResponse(raw, &resp); err != nil {
		return nil, fmt.Errorf("iLink getqrstatus decode: %w", err)
	}
	return &resp, nil
}
