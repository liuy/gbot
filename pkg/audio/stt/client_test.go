package stt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capturedUpload struct {
	form            map[string]string
	file            []byte
	auth            string
	content         string
	fileContentType string
	fileFilename    string
}

// captureServer records the multipart upload and replies with respBody.
func captureServer(t *testing.T, status int, respBody string) (*httptest.Server, *capturedUpload) {
	t.Helper()
	up := &capturedUpload{form: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("request path = %q, want /audio/transcriptions", r.URL.Path)
		}
		up.auth = r.Header.Get("Authorization")
		content := r.Header.Get("Content-Type")
		up.content = content
		mr, err := multipart.NewReader(r.Body, strings.TrimPrefix(content, "multipart/form-data; boundary=")).ReadForm(1 << 20)
		if err != nil {
			t.Errorf("read multipart: %v", err)
			return
		}
		for k, vs := range mr.Value {
			if len(vs) > 0 {
				up.form[k] = vs[0]
			}
		}
		if fhs := mr.File["file"]; len(fhs) != 1 {
			t.Errorf("file parts = %d, want exactly 1", len(fhs))
			return
		} else {
			up.fileContentType = fhs[0].Header.Get("Content-Type")
			up.fileFilename = fhs[0].Filename
			f, err := fhs[0].Open()
			if err != nil {
				t.Errorf("open file part: %v", err)
				return
			}
			up.file, err = io.ReadAll(f)
			_ = f.Close()
			if err != nil {
				t.Errorf("read file part: %v", err)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, up
}

func TestTranscribe_MultipartFields(t *testing.T) {
	srv, up := captureServer(t, 200, `{"text":"你好"}`)
	pcm := []byte{0x01, 0x02, 0x03, 0x04}
	audio := EncodeWavPCM16(pcm, 16000, 1)

	got, err := (&Client{}).Transcribe(context.Background(), Request{
		BaseURL: srv.URL, Model: "stt", APIKey: "k", Prompt: "gbot", Audio: audio,
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "你好" {
		t.Errorf("text = %q, want 你好", got)
	}
	if up.form["model"] != "stt" {
		t.Errorf("model field = %q, want stt", up.form["model"])
	}
	if up.form["prompt"] != "gbot" {
		t.Errorf("prompt field = %q, want gbot", up.form["prompt"])
	}
	if !bytes.Equal(up.file, audio) {
		t.Errorf("file part = %d bytes, want the exact WAV bytes (%d)", len(up.file), len(audio))
	}
	if !strings.Contains(up.content, "multipart/form-data") {
		t.Errorf("content type = %q, want multipart/form-data", up.content)
	}

	// Empty prompt must omit the field entirely, not send an empty value.
	srv2, up2 := captureServer(t, 200, `{"text":"ok"}`)
	if _, err := (&Client{}).Transcribe(context.Background(), Request{
		BaseURL: srv2.URL, Model: "stt", Audio: audio,
	}); err != nil {
		t.Fatalf("Transcribe without prompt: %v", err)
	}
	if _, present := up2.form["prompt"]; present {
		t.Errorf("prompt field present (%q) for empty prompt, want omitted", up2.form["prompt"])
	}
}

func TestTranscribe_EmptyTextIsValid(t *testing.T) {
	srv, _ := captureServer(t, 200, `{"text":"","timing":{"ttft_ms":100}}`)
	got, err := (&Client{}).Transcribe(context.Background(), Request{BaseURL: srv.URL, Model: "stt"})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "" {
		t.Errorf("text = %q, want empty", got)
	}
}

func TestTranscribe_AsrTextFallback(t *testing.T) {
	srv, _ := captureServer(t, 200, `{"text":"","asr_text":"fallback text"}`)
	got, err := (&Client{}).Transcribe(context.Background(), Request{BaseURL: srv.URL, Model: "stt"})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "fallback text" {
		t.Errorf("text = %q, want fallback text", got)
	}
}

func TestTranscribe_NestedErrorShape(t *testing.T) {
	srv, _ := captureServer(t, 400, `{"error":{"message":"live transcription requires a model configured with mode=streaming: stt","type":"invalid_request_error"}}`)
	_, err := (&Client{}).Transcribe(context.Background(), Request{BaseURL: srv.URL, Model: "stt"})
	if err == nil {
		t.Fatal("Transcribe should fail on 400")
	}
	if !strings.Contains(err.Error(), "mode=streaming") {
		t.Errorf("error = %v, want nested message content", err)
	}
}

func TestTranscribe_FlatErrorFallback(t *testing.T) {
	srv, _ := captureServer(t, 500, `{"message":"flat failure"}`)
	_, err := (&Client{}).Transcribe(context.Background(), Request{BaseURL: srv.URL, Model: "stt"})
	if err == nil || !strings.Contains(err.Error(), "flat failure") {
		t.Fatalf("error = %v, want flat message content", err)
	}
}

func TestTranscribe_ErrorNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	_, err := (&Client{}).Transcribe(context.Background(), Request{BaseURL: srv.URL, Model: "stt"})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v, want status-line fallback with 502", err)
	}
}

func TestTranscribe_AuthHeader(t *testing.T) {
	pcm := EncodeWavPCM16([]byte{0, 0}, 16000, 1)
	t.Run("key set", func(t *testing.T) {
		srv, up := captureServer(t, 200, `{"text":"x"}`)
		if _, err := (&Client{}).Transcribe(context.Background(), Request{BaseURL: srv.URL, Model: "stt", APIKey: "sk-1", Audio: pcm}); err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		if up.auth != "Bearer sk-1" {
			t.Errorf("Authorization = %q, want Bearer sk-1", up.auth)
		}
	})
	t.Run("no key omits header", func(t *testing.T) {
		srv, up := captureServer(t, 200, `{"text":"x"}`)
		if _, err := (&Client{}).Transcribe(context.Background(), Request{BaseURL: srv.URL, Model: "stt", Audio: pcm}); err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		if up.auth != "" {
			t.Errorf("Authorization = %q, want empty", up.auth)
		}
	})
}

func TestTranscribe_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bounded: the client aborts at 50ms, but the server-side context
		// does not reliably observe the disconnect while the body is
		// unconsumed, so the handler must also wake on its own.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := (&Client{}).Transcribe(ctx, Request{BaseURL: srv.URL, Model: "stt"})
	if err == nil {
		t.Fatal("Transcribe should fail on ctx timeout")
	}
}

func TestEncodeWavPCM16(t *testing.T) {
	t.Run("3 samples exact bytes", func(t *testing.T) {
		pcm := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
		wav := EncodeWavPCM16(pcm, 16000, 1)
		if len(wav) != 50 {
			t.Fatalf("len = %d, want 50", len(wav))
		}
		wantHeader := []byte{
			'R', 'I', 'F', 'F', 36 + 6, 0, 0, 0, // RIFF size = 36 + dataSize
			'W', 'A', 'V', 'E',
			'f', 'm', 't', ' ', 16, 0, 0, 0,
			1, 0, // PCM
			1, 0, // channels
			0x80, 0x3E, 0x00, 0x00, // 16000
			0x00, 0x7D, 0x00, 0x00, // byteRate 32000
			2, 0, // blockAlign
			16, 0, // bits
			'd', 'a', 't', 'a', 6, 0, 0, 0,
		}
		if !bytes.Equal(wav[:44], wantHeader) {
			t.Errorf("header = % X, want % X", wav[:44], wantHeader)
		}
		if !bytes.Equal(wav[44:], pcm) {
			t.Errorf("payload = % X, want % X", wav[44:], pcm)
		}
	})
	t.Run("empty input is bare header", func(t *testing.T) {
		wav := EncodeWavPCM16(nil, 16000, 1)
		if len(wav) != 44 {
			t.Fatalf("len = %d, want 44", len(wav))
		}
		if got := wav[4:8]; !bytes.Equal(got, []byte{36, 0, 0, 0}) {
			t.Errorf("RIFF size = % X, want 36 0 0 0", got)
		}
		if got := wav[40:44]; !bytes.Equal(got, []byte{0, 0, 0, 0}) {
			t.Errorf("data size = % X, want 0 0 0 0", got)
		}
	})
	t.Run("stereo doubles blockAlign and byteRate", func(t *testing.T) {
		wav := EncodeWavPCM16([]byte{0, 0, 0, 0}, 16000, 2)
		blockAlign := uint16(wav[32]) | uint16(wav[33])<<8
		byteRate := uint32(wav[28]) | uint32(wav[29])<<8 | uint32(wav[30])<<16 | uint32(wav[31])<<24
		if blockAlign != 4 {
			t.Errorf("blockAlign = %d, want 4", blockAlign)
		}
		if byteRate != 64000 {
			t.Errorf("byteRate = %d, want 64000", byteRate)
		}
	})
}

func TestTranscribe_PartContentType(t *testing.T) {
	t.Run("default is audio/wav", func(t *testing.T) {
		srv, up := captureServer(t, 200, `{"text":"x"}`)
		if _, err := (&Client{}).Transcribe(context.Background(), Request{
			BaseURL: srv.URL, Model: "stt", Audio: EncodeWavPCM16([]byte{0, 0}, 16000, 1),
		}); err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		if got := up.fileContentType; got != "audio/wav" {
			t.Errorf("file part Content-Type = %q, want audio/wav", got)
		}
	})
	t.Run("explicit mime wins", func(t *testing.T) {
		srv, up := captureServer(t, 200, `{"text":"x"}`)
		if _, err := (&Client{}).Transcribe(context.Background(), Request{
			BaseURL: srv.URL, Model: "stt", Audio: []byte{0}, Filename: "a.raw", MimeType: "audio/flac",
		}); err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		if got := up.fileContentType; got != "audio/flac" {
			t.Errorf("file part Content-Type = %q, want audio/flac", got)
		}
		if got := up.fileFilename; got != "a.raw" {
			t.Errorf("file part filename = %q, want a.raw", got)
		}
	})
}

func TestTranscribe_InvalidJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not json`))
	}))
	t.Cleanup(srv.Close)
	_, err := (&Client{}).Transcribe(context.Background(), Request{BaseURL: srv.URL, Model: "stt"})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("error = %v, want a decode-response failure", err)
	}
}

func TestTranscribe_TimeoutWrapsContextError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bounded: the server-side context does not observe the client
		// abort while the multipart body is unconsumed (net/http starts
		// its disconnect read only at body EOF).
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := (&Client{}).Transcribe(ctx, Request{BaseURL: srv.URL, Model: "stt"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want it to unwrap to context.DeadlineExceeded", err)
	}
}

func TestTranscribe_InvalidBaseURLFailsFast(t *testing.T) {
	_, err := (&Client{}).Transcribe(context.Background(), Request{
		BaseURL: "http://127.0.0.1:1\x7f", Model: "stt",
	})
	if err == nil || !strings.Contains(err.Error(), "stt request") {
		t.Fatalf("error = %v, want a request-construction failure", err)
	}
}
