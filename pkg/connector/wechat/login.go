package wechat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	qrterminal "github.com/mdp/qrterminal/v3"
)

// Login runs the interactive iLink QR login flow.
// On success, saves state to ~/.gbot/wechat/state.json.
// Returns the account ID on success.
func Login(ctx context.Context, client *http.Client) (string, error) {
	// Step 1: Fetch QR code
	slog.Info("wechat: fetching QR code for login")

	qrResp, err := GetBotQR(ctx, client, "3")
	if err != nil {
		return "", fmt.Errorf("fetch QR code: %w", err)
	}

	qrcodeValue := qrResp.QRCode
	qrcodeURL := qrResp.QRCodeImg
	if qrcodeValue == "" {
		return "", fmt.Errorf("QR response missing qrcode")
	}

	fmt.Println("\n请使用微信扫描以下二维码：")
	qrterminal.GenerateHalfBlock(qrcodeURL, qrterminal.L, os.Stdout)
	fmt.Printf("\n（无法扫码？直接打开此链接：%s）\n", qrcodeURL)

	// Step 2: Poll for QR status
	deadline := time.Now().Add(480 * time.Second)
	currentBaseURL := ILINKBaseURL
	refreshCount := 0

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		statusResp, err := GetQRStatus(ctx, client, currentBaseURL, qrcodeValue)
		if err != nil {
			slog.Warn("wechat: QR poll error", "error", err)
			time.Sleep(1 * time.Second)
			continue
		}

		status := statusResp.Status
		if status == "" {
			status = "wait"
		}

		switch status {
		case "wait":
			fmt.Print(".")
			// Flush by printing to ensure visibility
		case "scaned":
			fmt.Println("\n已扫码，请在微信里确认...")
		case "scaned_but_redirect":
			if statusResp.RedirectHost != "" {
				currentBaseURL = "https://" + statusResp.RedirectHost
			}
		case "expired":
			refreshCount++
			if refreshCount > 3 {
				fmt.Println("\n二维码多次过期，请重新执行登录。")
				return "", fmt.Errorf("QR code expired too many times")
			}
			fmt.Printf("\n二维码已过期，正在刷新... (%d/3)\n", refreshCount)
			newQRResp, err := GetBotQR(ctx, client, "3")
			if err != nil {
				return "", fmt.Errorf("QR refresh: %w", err)
			}
			qrcodeValue = newQRResp.QRCode
			qrcodeURL = newQRResp.QRCodeImg
			if qrcodeURL != "" {
				qrterminal.GenerateHalfBlock(qrcodeURL, qrterminal.L, os.Stdout)
			}
		case "confirmed":
			accountID := statusResp.ILinkBotID
			token := statusResp.BotToken
			baseURL := statusResp.BaseURL
			userID := statusResp.ILinkUserID

			if baseURL == "" {
				baseURL = ILINKBaseURL
			}

			if accountID == "" || token == "" {
				return "", fmt.Errorf("QR confirmed but credential payload was incomplete")
			}

			state := &State{
				AccountID: accountID,
				Token:     token,
				BaseURL:   baseURL,
				SyncBuf:   "",
			}
			// Include user_id in the context_tokens to track which user this bot is
			if userID != "" {
				if state.ContextTokens == nil {
					state.ContextTokens = make(map[string]string)
				}
				state.ContextTokens["_self_user_id"] = userID
			}

			if err := SaveState(state); err != nil {
				return "", fmt.Errorf("save state: %w", err)
			}

			fmt.Printf("\n微信连接成功，account_id=%s\n", accountID)
			return accountID, nil
		}

		time.Sleep(1 * time.Second)
	}

	fmt.Println("\n微信登录超时。")
	return "", fmt.Errorf("QR login timed out")
}
