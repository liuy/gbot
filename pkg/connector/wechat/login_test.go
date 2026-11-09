package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// pinLoginTransport serves the iLink QR endpoints without touching the
// network, and records every request URL so a redirect can be verified.
type pinLoginTransport struct {
	qrBodies     []string
	statusBodies []string
	requests     []string
	qrIdx        int
	stIdx        int
	err          error
	qrErr        error // fail QR fetches from qrFailAfter onward
	qrFailAfter  int
}

func (tr *pinLoginTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if tr.err != nil {
		return nil, tr.err
	}
	tr.requests = append(tr.requests, r.URL.String())
	var body string
	if strings.Contains(r.URL.Path, "get_bot_qrcode") {
		if tr.qrErr != nil && tr.qrIdx >= tr.qrFailAfter {
			return nil, tr.qrErr
		}
		if tr.qrIdx < len(tr.qrBodies) {
			body = tr.qrBodies[tr.qrIdx]
			tr.qrIdx++
		} else {
			body = tr.qrBodies[len(tr.qrBodies)-1]
		}
	} else {
		if tr.stIdx < len(tr.statusBodies) {
			body = tr.statusBodies[tr.stIdx]
			tr.stIdx++
		} else {
			body = `{"status":"wait"}`
		}
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	return resp, nil
}

func pinQRBody(code, img string) string {
	b, _ := json.Marshal(GetBotQRResponse{QRCode: code, QRCodeImg: img})
	return string(b)
}

func pinStatusBody(s GetQRStatusResponse) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// pinCaptureStdout redirects os.Stdout into a temp file (a pipe would block
// under synctest) and returns what was written.
func pinCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create stdout file: %v", err)
	}
	old := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = old }()
	fn()
	if err := f.Close(); err != nil {
		t.Fatalf("close stdout file: %v", err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read stdout file: %v", err)
	}
	return string(data)
}

// pinDotCount counts the status dots: lines made of nothing but ".".
func pinDotCount(out string) int {
	n := 0
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" || strings.ContainsAny(line, "█▀") {
			continue
		}
		if strings.Trim(line, ".") == "" {
			n += len(line)
		}
	}
	return n
}

// pinQRBlockCount counts rendered QR blocks (each ends in a full ▀ rule).
func pinQRBlockCount(out string) int {
	n := 0
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" {
			continue
		}
		allBars := true
		for _, r := range line {
			if r != '▀' {
				allBars = false
				break
			}
		}
		if allBars {
			n++
		}
	}
	return n
}

// The happy path: QR prompt on stdout, status dots, the redirect host adopted
// for later polls, and the confirmed credentials written to the state file.
func TestPinLoginConfirmedFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &pinLoginTransport{
			qrBodies: []string{pinQRBody("QR1", "http://qrimg")},
			statusBodies: []string{
				pinStatusBody(GetQRStatusResponse{}),
				pinStatusBody(GetQRStatusResponse{Status: "wait"}),
				pinStatusBody(GetQRStatusResponse{Status: "scaned"}),
				pinStatusBody(GetQRStatusResponse{Status: "scaned_but_redirect", RedirectHost: "redirect.example.com"}),
				pinStatusBody(GetQRStatusResponse{Status: "confirmed", ILinkBotID: "bot", BotToken: "tok", BaseURL: "https://base.example.com", ILinkUserID: "user-9"}),
			},
		}
		client := &http.Client{Transport: tr}
		projectDir := t.TempDir()
		var accountID string
		var err error
		var logs string
		var elapsed time.Duration
		out := pinCaptureStdout(t, func() {
			logs = pinCaptureSlog(t, func() {
				start := time.Now()
				accountID, err = Login(context.Background(), client, projectDir)
				elapsed = time.Since(start)
			})
		})
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		if accountID != "bot" {
			t.Errorf("accountID = %q, want bot", accountID)
		}
		if !strings.Contains(logs, "fetching QR code for login") {
			t.Errorf("logs = %q, want the QR fetch line", logs)
		}
		for _, want := range []string{"请使用微信扫描以下二维码", "http://qrimg", "已扫码", "微信连接成功，account_id=bot"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q", want)
			}
		}
		// One dot per non-terminal poll: the empty status counts as "wait".
		if got := pinDotCount(out); got != 2 {
			t.Errorf("status dots = %d, want 2 (empty status and wait); stdout=%q", got, out)
		}
		// The redirect host must be adopted for the poll that follows it.
		if len(tr.requests) != 6 {
			t.Fatalf("requests = %d (%v), want 1 QR + 5 status", len(tr.requests), tr.requests)
		}
		if want := "https://redirect.example.com/"; !strings.HasPrefix(tr.requests[5], want) {
			t.Errorf("last status request = %q, want it to target %q", tr.requests[5], want)
		}
		if !strings.HasPrefix(tr.requests[4], ILINKBaseURL) {
			t.Errorf("fourth status request = %q, want the default iLink base URL", tr.requests[4])
		}
		// Four non-terminal statuses at 1s each; confirmed returns at once.
		if elapsed != 4*time.Second {
			t.Errorf("virtual elapsed = %v, want 4s (one 1s pause per non-terminal status)", elapsed)
		}
		st, err := LoadState("bot", projectDir)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st == nil {
			t.Fatal("state file not written by Login")
		}
		if st.Token != "tok" || st.BaseURL != "https://base.example.com" {
			t.Errorf("saved state = %+v, want the confirmed token and baseurl", st)
		}
		if st.ContextTokens["_self_user_id"] != "user-9" {
			t.Errorf("saved ContextTokens = %v, want _self_user_id=user-9", st.ContextTokens)
		}
	})
}

// qrMaxRefresh expirations must refresh the QR (new code and new image) and
// still succeed; the next one must give up.
func TestPinLoginExpiredRefreshes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Fixture and expectations both come from qrMaxRefresh, so the test
		// still lands on the boundary if the constant moves.
		rounds := qrMaxRefresh + 1
		qrBodies := make([]string, rounds)
		statusBodies := make([]string, rounds)
		for i := range qrMaxRefresh {
			qrBodies[i] = pinQRBody(fmt.Sprintf("QR%d", i+1), fmt.Sprintf("https://qr.example.com/img%d", i+1))
			statusBodies[i] = pinStatusBody(GetQRStatusResponse{Status: "expired"})
		}
		qrBodies[rounds-1] = pinQRBody(fmt.Sprintf("QR%d", rounds), fmt.Sprintf("https://qr.example.com/img%d", rounds))
		statusBodies[rounds-1] = pinStatusBody(GetQRStatusResponse{Status: "confirmed", ILinkBotID: "b@im.bot", BotToken: "t"})
		tr := &pinLoginTransport{qrBodies: qrBodies, statusBodies: statusBodies}
		projectDir := t.TempDir()
		var err error
		out := pinCaptureStdout(t, func() {
			_, err = Login(context.Background(), &http.Client{Transport: tr}, projectDir)
		})
		if err != nil {
			t.Fatalf("Login with %d expirations: %v (stdout=%q)", qrMaxRefresh, err, out)
		}
		wants := make([]string, 0, rounds)
		for i := range qrMaxRefresh {
			wants = append(wants, fmt.Sprintf("(%d/%d)", i+1, qrMaxRefresh))
		}
		wants = append(wants, "二维码已过期，正在刷新")
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q", want)
			}
		}
		if pinQRBlockCount(out) != rounds {
			t.Errorf("QR blocks = %d, want %d (the initial code plus one per refresh)", pinQRBlockCount(out), rounds)
		}
		var polls []string
		for _, r := range tr.requests {
			if _, after, ok := strings.Cut(r, "qrcode="); ok {
				polls = append(polls, after)
			}
		}
		if len(polls) != rounds {
			t.Fatalf("status polls = %v, want %d", polls, rounds)
		}
		for i := range polls {
			w := fmt.Sprintf("QR%d", i+1)
			if polls[i] != w {
				t.Errorf("poll[%d] qrcode = %q, want %q", i, polls[i], w)
			}
		}
	})
}

func TestPinLoginExpiredTooManyTimes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &pinLoginTransport{
			qrBodies: []string{
				pinQRBody("QR1", "https://qr.example.com/img1"),
				pinQRBody("QR2", ""),
			},
			statusBodies: []string{
				pinStatusBody(GetQRStatusResponse{Status: "expired"}),
				pinStatusBody(GetQRStatusResponse{Status: "expired"}),
				pinStatusBody(GetQRStatusResponse{Status: "expired"}),
				pinStatusBody(GetQRStatusResponse{Status: "expired"}),
			},
		}
		var err error
		out := pinCaptureStdout(t, func() {
			_, err = Login(context.Background(), &http.Client{Transport: tr}, t.TempDir())
		})
		if err == nil {
			t.Fatalf("Login with 4 expirations = nil error, want the give-up error; stdout=%q", out)
		}
		if !strings.Contains(err.Error(), "QR code expired too many times") {
			t.Errorf("Login error = %q, want the give-up message", err)
		}
		if !strings.Contains(out, "二维码多次过期") {
			t.Errorf("stdout missing the give-up notice")
		}
		// Every refresh came back without an image, so only the first QR renders.
		if got := pinQRBlockCount(out); got != 1 {
			t.Errorf("QR blocks = %d, want 1 (an empty qrcode_img_content must not render)", got)
		}
	})
}

// A QR response without a code cannot start the flow.
func TestPinLoginMissingQRCode(t *testing.T) {
	tr := &pinLoginTransport{qrBodies: []string{pinQRBody("", "https://qr.example.com/img")}}
	_, err := Login(context.Background(), &http.Client{Transport: tr}, t.TempDir())
	if err == nil {
		t.Fatal("Login with an empty qrcode = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "QR response missing qrcode") {
		t.Errorf("Login error = %q, want the missing-qrcode message", err)
	}
}

// The QR fetch failure must stay wrapped so callers can classify it.
func TestPinLoginFetchErrorWrap(t *testing.T) {
	pin := errors.New("qr fetch boom")
	tr := &pinLoginTransport{err: pin}
	_, err := Login(context.Background(), &http.Client{Transport: tr}, t.TempDir())
	if err == nil {
		t.Fatal("Login with a failing QR fetch = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "fetch QR code:") {
		t.Errorf("Login error = %q, want the fetch prefix", err)
	}
	if !errors.Is(err, pin) {
		t.Errorf("Login error = %q, want it to wrap the transport error", err)
	}
}

// A confirmed response missing either credential is an error, not a half-saved
// account.
func TestPinLoginIncompleteCredentials(t *testing.T) {
	cases := []GetQRStatusResponse{
		{Status: "confirmed", ILinkBotID: "", BotToken: "tok"},
		{Status: "confirmed", ILinkBotID: "bot@im.bot", BotToken: ""},
	}
	for _, tc := range cases {
		tr := &pinLoginTransport{
			qrBodies:     []string{pinQRBody("QR1", "https://qr.example.com/img")},
			statusBodies: []string{pinStatusBody(tc)},
		}
		projectDir := t.TempDir()
		var err error
		pinCaptureStdout(t, func() {
			_, err = Login(context.Background(), &http.Client{Transport: tr}, projectDir)
		})
		if err == nil {
			t.Fatalf("Login with %+v = nil error, want the incomplete-payload failure", tc)
		}
		if !strings.Contains(err.Error(), "credential payload was incomplete") {
			t.Errorf("Login error = %q, want the incomplete-payload message", err)
		}
		if _, statErr := os.Stat(filepath.Join(projectDir, "wechat")); statErr == nil {
			t.Errorf("Login with %+v wrote state under %q, want nothing saved", tc, projectDir)
		}
	}
}

// A QR refresh failure aborts login and keeps the transport error wrapped.
func TestPinLoginRefreshFailureWrap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pin := errors.New("refresh boom")
		tr := &pinLoginTransport{
			qrBodies:     []string{pinQRBody("QR1", "http://qrimg")},
			statusBodies: []string{pinStatusBody(GetQRStatusResponse{Status: "expired"})},
			qrErr:        pin,
			qrFailAfter:  1,
		}
		var err error
		pinCaptureStdout(t, func() {
			_, err = Login(context.Background(), &http.Client{Transport: tr}, t.TempDir())
		})
		if err == nil {
			t.Fatal("Login with a failing QR refresh = nil error, want failure")
		}
		if !strings.Contains(err.Error(), "QR refresh:") {
			t.Errorf("Login error = %q, want the refresh prefix", err)
		}
		if !errors.Is(err, pin) {
			t.Errorf("Login error = %q, want it to wrap the refresh error", err)
		}
	})
}

// A confirmed response without a baseurl falls back to the default iLink host.
func TestPinLoginDefaultBaseURL(t *testing.T) {
	tr := &pinLoginTransport{
		qrBodies:     []string{pinQRBody("QR1", "http://qrimg")},
		statusBodies: []string{pinStatusBody(GetQRStatusResponse{Status: "confirmed", ILinkBotID: "bot", BotToken: "tok"})},
	}
	projectDir := t.TempDir()
	pinCaptureStdout(t, func() {
		if _, err := Login(context.Background(), &http.Client{Transport: tr}, projectDir); err != nil {
			t.Fatalf("Login: %v", err)
		}
	})
	st, err := LoadState("bot", projectDir)
	if err != nil || st == nil {
		t.Fatalf("LoadState = (%+v, %v), want the saved state", st, err)
	}
	if st.BaseURL != ILINKBaseURL {
		t.Errorf("saved BaseURL = %q, want the default %q", st.BaseURL, ILINKBaseURL)
	}
}

// A state-write failure must abort login with the save-state context.
func TestPinLoginSaveStateError(t *testing.T) {
	tr := &pinLoginTransport{
		qrBodies:     []string{pinQRBody("QR1", "https://qr.example.com/img")},
		statusBodies: []string{pinStatusBody(GetQRStatusResponse{Status: "confirmed", ILinkBotID: "bot@im.bot", BotToken: "tok"})},
	}
	projectDir := t.TempDir()
	// <projectDir>/wechat is a regular file: MkdirAll cannot create it.
	if err := os.WriteFile(filepath.Join(projectDir, "wechat"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	_, err := Login(context.Background(), &http.Client{Transport: tr}, projectDir)
	if err == nil {
		t.Fatal("Login with an unwritable state dir = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "save state:") {
		t.Errorf("Login error = %q, want the save-state prefix", err)
	}
	if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Errorf("Login error = %q, want it to wrap the *os.PathError from the state write", err)
	}
}

// A cancelled context returns the context error, not a timeout.
func TestPinLoginContextCancelled(t *testing.T) {
	tr := &pinLoginTransport{
		qrBodies:     []string{pinQRBody("QR1", "https://qr.example.com/img")},
		statusBodies: []string{pinStatusBody(GetQRStatusResponse{Status: "wait"})},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Login(ctx, &http.Client{Transport: tr}, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Login(cancelled ctx) error = %v, want context.Canceled", err)
	}
}

// A status poll failure warns, waits one second and keeps polling.
func TestPinLoginPollErrorRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &pinLoginTransport{
			qrBodies: []string{pinQRBody("QR1", "https://qr.example.com/img")},
			statusBodies: []string{
				pinStatusBody(GetQRStatusResponse{Status: "confirmed", ILinkBotID: "b@im.bot", BotToken: "t"}),
			},
		}
		// Fail the first poll, then serve the scripted sequence.
		var failed int
		wrapped := &pinLoginErrOnce{inner: tr, fail: &failed}
		var err error
		var logs string
		pinCaptureStdout(t, func() {
			logs = pinCaptureSlog(t, func() {
				start := time.Now()
				_, err = Login(context.Background(), &http.Client{Transport: wrapped}, t.TempDir())
				if got := time.Since(start); got != 1*time.Second {
					t.Errorf("elapsed after one poll failure = %v, want 1s (the retry pause)", got)
				}
			})
		})
		if err != nil {
			t.Fatalf("Login after one poll failure: %v", err)
		}
		if !strings.Contains(logs, "QR poll error") {
			t.Errorf("logs = %q, want the poll-error warning", logs)
		}
	})
}

// pinLoginErrOnce fails the first status poll, then delegates.
type pinLoginErrOnce struct {
	inner *pinLoginTransport
	fail  *int
}

func (tr *pinLoginErrOnce) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "get_qrcode_status") && *tr.fail == 0 {
		*tr.fail++
		return nil, errors.New("poll boom")
	}
	return tr.inner.RoundTrip(r)
}

// The pacing constants are the single source the tests now read, so the values
// themselves need a literal pin: iLink's QR expiry window and the refresh cap
// the progress line prints are protocol-facing numbers, not free choices.
func TestPinLoginPacingConstants(t *testing.T) {
	if qrLoginWindow != 480*time.Second || qrPollEvery != time.Second || qrMaxRefresh != 3 {
		t.Errorf("QR pacing = (%v, %v, %d), want 480s / 1s / 3", qrLoginWindow, qrPollEvery, qrMaxRefresh)
	}
}

// Past qrLoginWindow the flow reports a timeout.
func TestPinLoginDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &pinLoginTransport{
			qrBodies:     []string{pinQRBody("QR1", "https://qr.example.com/img")},
			statusBodies: []string{pinStatusBody(GetQRStatusResponse{Status: "wait"})},
		}
		var err error
		var out string
		start := time.Now()
		out = pinCaptureStdout(t, func() {
			_, err = Login(context.Background(), &http.Client{Transport: tr}, t.TempDir())
		})
		if elapsed := time.Since(start); elapsed != qrLoginWindow {
			t.Errorf("Login window = %v, want %v", elapsed, qrLoginWindow)
		}
		if err == nil || !strings.Contains(err.Error(), "QR login timed out") {
			t.Errorf("Login error = %v, want the timeout message", err)
		}
		if !strings.Contains(out, "微信登录超时") {
			t.Errorf("stdout missing the timeout notice")
		}
	})
}
