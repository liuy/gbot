// Package tts implements a streaming synthesis client for OpenAI-compatible
// audio endpoints (audio.cpp /v1/audio/speech, SSE-framed PCM), used by the
// wui voice bridge.
package tts

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// SampleRate pins the wire contract: audio.cpp speech PCM is s16le mono 24kHz.
const SampleRate = 24000

// Request is one synthesis call. Voice is optional: when empty the field is
// omitted and the server's default_voice_preset applies.
type Request struct {
	BaseURL string
	Model   string
	APIKey  string
	Text    string
	// Seed pins codec sampling when non-zero; zero omits the field and the
	// engine draws a random seed — the clone path's stable setting.
	Seed int
	// Voice is a backend speaker name; empty omits the field so the
	// engine's default preset applies.
	Voice string
}

// Client posts speech requests. A nil HTTP client means http.DefaultClient.
type Client struct {
	HTTP *http.Client
}

// Stream synthesizes req.Text and feeds each decoded PCM chunk to onPCM in
// arrival order. The stream ends after speech.audio.done (or [DONE]); an
// upstream error event or non-2xx status fails the call.
func (c *Client) Stream(ctx context.Context, req Request, onPCM func([]byte)) error {
	body, err := json.Marshal(struct {
		Model          string `json:"model"`
		Input          string `json:"input"`
		Voice          string `json:"voice,omitempty"`
		ResponseFormat string `json:"response_format"`
		StreamFormat   string `json:"stream_format"`
		Seed           int    `json:"seed,omitempty"`
	}{
		Model:          req.Model,
		Input:          req.Text,
		Voice:          req.Voice,
		ResponseFormat: "pcm",
		StreamFormat:   "sse",
		Seed:           req.Seed,
	})
	if err != nil {
		return fmt.Errorf("tts: encode body: %w", err)
	}

	url := strings.TrimRight(req.BaseURL, "/") + "/audio/speech"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("tts: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if req.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	}

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("tts: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("tts: %s", upstreamError(resp.StatusCode, data))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			return nil
		}
		// Wire shape verified against the live audio.cpp: the delta event
		// carries the base64 PCM in an "audio" field (not "delta" — the
		// ASR delta events do; do not conflate the two).
		var ev struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "speech.audio.delta":
			pcm, err := base64.StdEncoding.DecodeString(ev.Audio)
			if err != nil {
				return fmt.Errorf("tts: decode audio delta: %w", err)
			}
			onPCM(pcm)
		case "speech.audio.done":
			return nil
		case "error":
			msg := "upstream error"
			if ev.Error != nil && ev.Error.Message != "" {
				msg = ev.Error.Message
			}
			return fmt.Errorf("tts: %s", msg)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("tts: read stream: %w", err)
	}
	return nil
}

// Synthesize runs Stream and collects all PCM chunks into one buffer (the
// filler path caches exactly this).
func (c *Client) Synthesize(ctx context.Context, req Request) ([]byte, error) {
	var out []byte
	err := c.Stream(ctx, req, func(pcm []byte) {
		out = append(out, pcm...)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Voices lists the speaker catalog the engine exposes for the model
// (audio.cpp GET /audio/voices?model=). The names are backend knowledge:
// this only relays them for a settings picker, never interprets them.
func (c *Client) Voices(ctx context.Context, req Request) ([]string, error) {
	url := strings.TrimRight(req.BaseURL, "/") + "/audio/voices?model=" + url.QueryEscape(req.Model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("tts: voices request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	if req.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("tts: voices: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("tts: voices body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tts: voices: %s", upstreamError(resp.StatusCode, data))
	}
	var payload struct {
		Voices []string `json:"voices"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("tts: voices decode: %w", err)
	}
	return payload.Voices, nil
}

// upstreamError extracts the message from audio.cpp's nested
// {"error":{"message":...}} shape, falling back to a flat {"message":...}
// and finally to a bare status line (same shape as the stt client).
func upstreamError(status int, data []byte) string {
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &nested) == nil {
		if nested.Error.Message != "" {
			return nested.Error.Message
		}
		if nested.Message != "" {
			return nested.Message
		}
	}
	return fmt.Sprintf("speech HTTP %d", status)
}
