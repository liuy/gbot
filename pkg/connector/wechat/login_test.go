package wechat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// loginTestRoundTripper redirects all requests to a local httptest server.
type loginTestRoundTripper struct {
	mu      sync.Mutex
	handler http.HandlerFunc
}

func (rt *loginTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	h := rt.handler
	rt.mu.Unlock()

	w := httptest.NewRecorder()
	h(w, req)
	return w.Result(), nil
}

// loginTestClient builds an http.Client that routes all requests to handler.
func loginTestClient(handler http.HandlerFunc) *http.Client {
	return &http.Client{
		Transport: &loginTestRoundTripper{handler: handler},
	}
}

func TestLogin_Success(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		callCount := 0
		handler := func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			callCount++

			path := r.URL.Path + "?" + r.URL.RawQuery
			if strings.Contains(path, "get_bot_qrcode") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"qrcode":"qr123","qrcode_img":"https://qr.example.com/img"}`)
				return
			}
			if strings.Contains(path, "get_qrcode_status") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"status":"confirmed","ilink_bot_id":"bot1","bot_token":"tok1","base_url":"https://api.test","ilink_user_id":"user1"}`)
				return
			}
			w.WriteHeader(404)
		}

		projectDir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var gotID string
		var gotErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			gotID, gotErr = Login(ctx, loginTestClient(handler), projectDir)
		}()

		// Let the goroutine run through the QR fetch and status poll.
		time.Sleep(5 * time.Second)
		cancel()
		<-done

		if gotErr != nil {
			t.Fatalf("Login returned error: %v", gotErr)
		}
		if gotID != "bot1" {
			t.Errorf("accountID = %q, want bot1", gotID)
		}
		mu.Lock()
		defer mu.Unlock()
		if callCount < 2 {
			t.Errorf("expected >= 2 API calls (getbotqr + getqrstatus), got %d", callCount)
		}
	})
}

func TestLogin_Timeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler := func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path + "?" + r.URL.RawQuery
			if strings.Contains(path, "get_bot_qrcode") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"qrcode":"qr123","qrcode_img":"https://qr.example.com/img"}`)
				return
			}
			if strings.Contains(path, "get_qrcode_status") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"status":"wait"}`)
				return
			}
			w.WriteHeader(404)
		}

		projectDir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var gotErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, gotErr = Login(ctx, loginTestClient(handler), projectDir)
		}()

		// Login has a 480s deadline. Advance past it in virtual time.
		time.Sleep(490 * time.Second)
		<-done

		if gotErr == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(gotErr.Error(), "timed out") {
			t.Errorf("error = %v, want timeout", gotErr)
		}
	})
}

func TestLogin_QRConfirmedWithCustomBaseURL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler := func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path + "?" + r.URL.RawQuery
			if strings.Contains(path, "get_bot_qrcode") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"qrcode":"qr123","qrcode_img":"https://qr.example.com/img"}`)
				return
			}
			if strings.Contains(path, "get_qrcode_status") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"status":"confirmed","ilink_bot_id":"bot2","bot_token":"tok2","base_url":"https://custom.api","ilink_user_id":"u2"}`)
				return
			}
			w.WriteHeader(404)
		}

		projectDir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var gotID string
		var gotErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			gotID, gotErr = Login(ctx, loginTestClient(handler), projectDir)
		}()

		time.Sleep(5 * time.Second)
		cancel()
		<-done

		if gotErr != nil {
			t.Fatalf("Login error: %v", gotErr)
		}
		if gotID != "bot2" {
			t.Errorf("accountID = %q, want bot2", gotID)
		}
	})
}

func TestLogin_IncompleteCredentials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler := func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path + "?" + r.URL.RawQuery
			if strings.Contains(path, "get_bot_qrcode") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"qrcode":"qr123","qrcode_img":"https://qr.example.com/img"}`)
				return
			}
			if strings.Contains(path, "get_qrcode_status") {
				w.Header().Set("Content-Type", "application/json")
				// Confirmed but missing token.
				_, _ = fmt.Fprint(w, `{"ret":0,"status":"confirmed","ilink_bot_id":"bot3"}`)
				return
			}
			w.WriteHeader(404)
		}

		projectDir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var gotErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, gotErr = Login(ctx, loginTestClient(handler), projectDir)
		}()

		time.Sleep(5 * time.Second)
		cancel()
		<-done

		if gotErr == nil {
			t.Fatal("expected error for incomplete credentials, got nil")
		}
		if !strings.Contains(gotErr.Error(), "incomplete") {
			t.Errorf("error = %v, want 'incomplete'", gotErr)
		}
	})
}

func TestLogin_QRExpired_TooManyTimes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler := func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path + "?" + r.URL.RawQuery
			if strings.Contains(path, "get_bot_qrcode") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"qrcode":"qr_new","qrcode_img":"https://qr.example.com/new"}`)
				return
			}
			if strings.Contains(path, "get_qrcode_status") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"status":"expired"}`)
				return
			}
			w.WriteHeader(404)
		}

		projectDir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var gotErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, gotErr = Login(ctx, loginTestClient(handler), projectDir)
		}()

		// 4 expires × 1s sleep = 4s. Plus some margin.
		time.Sleep(10 * time.Second)
		cancel()
		<-done

		if gotErr == nil {
			t.Fatal("expected error for too many expired QR codes")
		}
		if !strings.Contains(gotErr.Error(), "expired too many times") {
			t.Errorf("error = %v, want 'expired too many times'", gotErr)
		}
	})
}

func TestLogin_QRExpired_ThenConfirmed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		statusCalls := 0
		handler := func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path + "?" + r.URL.RawQuery
			if strings.Contains(path, "get_bot_qrcode") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"qrcode":"qr123","qrcode_img":"https://qr.example.com/img"}`)
				return
			}
			if strings.Contains(path, "get_qrcode_status") {
				mu.Lock()
				statusCalls++
				n := statusCalls
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if n <= 2 {
					_, _ = fmt.Fprint(w, `{"ret":0,"status":"expired"}`)
				} else {
					_, _ = fmt.Fprint(w, `{"ret":0,"status":"confirmed","ilink_bot_id":"bot4","bot_token":"tok4"}`)
				}
				return
			}
			w.WriteHeader(404)
		}

		projectDir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var gotID string
		var gotErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			gotID, gotErr = Login(ctx, loginTestClient(handler), projectDir)
		}()

		time.Sleep(10 * time.Second)
		cancel()
		<-done

		if gotErr != nil {
			t.Fatalf("Login error: %v", gotErr)
		}
		if gotID != "bot4" {
			t.Errorf("accountID = %q, want bot4", gotID)
		}
	})
}

func TestLogin_GetQRStatusError_Retries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		statusCalls := 0
		handler := func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path + "?" + r.URL.RawQuery
			if strings.Contains(path, "get_bot_qrcode") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"qrcode":"qr123","qrcode_img":"https://qr.example.com/img"}`)
				return
			}
			if strings.Contains(path, "get_qrcode_status") {
				mu.Lock()
				statusCalls++
				n := statusCalls
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if n <= 1 {
					// Return an error response.
					w.WriteHeader(500)
					return
				}
				_, _ = fmt.Fprint(w, `{"ret":0,"status":"confirmed","ilink_bot_id":"bot5","bot_token":"tok5"}`)
				return
			}
			w.WriteHeader(404)
		}

		projectDir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var gotID string
		var gotErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			gotID, gotErr = Login(ctx, loginTestClient(handler), projectDir)
		}()

		time.Sleep(10 * time.Second)
		cancel()
		<-done

		if gotErr != nil {
			t.Fatalf("Login error: %v", gotErr)
		}
		if gotID != "bot5" {
			t.Errorf("accountID = %q, want bot5", gotID)
		}
	})
}

func TestLogin_StateSavedCorrectly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler := func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path + "?" + r.URL.RawQuery
			if strings.Contains(path, "get_bot_qrcode") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"qrcode":"qr123","qrcode_img":"https://qr.example.com/img"}`)
				return
			}
			if strings.Contains(path, "get_qrcode_status") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"ret":0,"status":"confirmed","ilink_bot_id":"saved_bot","bot_token":"saved_tok","baseurl":"https://saved.api","ilink_user_id":"saved_user"}`)
				return
			}
			w.WriteHeader(404)
		}

		projectDir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var gotID string
		done := make(chan struct{})
		go func() {
			defer close(done)
			gotID, _ = Login(ctx, loginTestClient(handler), projectDir)
		}()

		time.Sleep(5 * time.Second)
		cancel()
		<-done

		if gotID != "saved_bot" {
			t.Fatalf("accountID = %q, want saved_bot", gotID)
		}

		// Verify state was saved to the per-account path.
		state, err := LoadState("saved_bot", projectDir)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if state == nil {
			t.Fatal("state is nil")
		}
		if state.Token != "saved_tok" {
			t.Errorf("Token = %q, want saved_tok", state.Token)
		}
		if state.BaseURL != "https://saved.api" {
			t.Errorf("BaseURL = %q, want https://saved.api", state.BaseURL)
		}
		if state.ContextTokens["_self_user_id"] != "saved_user" {
			t.Errorf("ContextTokens[_self_user_id] = %q, want saved_user", state.ContextTokens["_self_user_id"])
		}
	})
}
