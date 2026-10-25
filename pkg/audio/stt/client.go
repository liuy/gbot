// Package stt implements a batch transcription client for OpenAI-compatible
// audio endpoints (audio.cpp /v1/audio/transcriptions), used by the wui STT
// bridge as the fallback path when no streaming model entry is available.
package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
)

// Request is one batch transcription call. Audio is a complete WAV file (see
// EncodeWavPCM16); Filename/MimeType default to audio.wav/audio.wav.
type Request struct {
	BaseURL  string
	Model    string
	APIKey   string
	Prompt   string
	Audio    []byte
	Filename string
	MimeType string
}

// Client posts transcription requests. A nil HTTP client means
// http.DefaultClient.
type Client struct {
	HTTP *http.Client
}

// Transcribe uploads req.Audio as multipart/form-data and returns the
// transcript text. An empty transcript is legal and returns ("", nil) — only
// transport and non-2xx responses are errors.
func (c *Client) Transcribe(ctx context.Context, req Request) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	filename := req.Filename
	if filename == "" {
		filename = "audio.wav"
	}
	mimeType := req.MimeType
	if mimeType == "" {
		mimeType = "audio/wav"
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="file"; filename="%s"`, strings.ReplaceAll(filename, `"`, "'")))
	h.Set("Content-Type", mimeType)
	fw, err := mw.CreatePart(h)
	if err != nil {
		return "", fmt.Errorf("stt multipart file: %w", err)
	}
	if _, err := fw.Write(req.Audio); err != nil {
		return "", fmt.Errorf("stt multipart write: %w", err)
	}
	if err := mw.WriteField("model", req.Model); err != nil {
		return "", fmt.Errorf("stt multipart model: %w", err)
	}
	if req.Prompt != "" {
		if err := mw.WriteField("prompt", req.Prompt); err != nil {
			return "", fmt.Errorf("stt multipart prompt: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("stt multipart close: %w", err)
	}

	url := strings.TrimRight(req.BaseURL, "/") + "/audio/transcriptions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", fmt.Errorf("stt request: %w", err)
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	if req.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	}

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("stt post: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("stt read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("stt: %s", upstreamError(resp.StatusCode, data))
	}

	var parsed struct {
		Text    string `json:"text"`
		AsrText string `json:"asr_text"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("stt decode response: %w", err)
	}
	if parsed.Text != "" {
		return parsed.Text, nil
	}
	// audio.cpp reports empty text via asr_text on some paths; both empty is
	// a legal empty transcript.
	return parsed.AsrText, nil
}

// upstreamError extracts the message from audio.cpp's nested
// {"error":{"message":...}} shape, falling back to a flat {"message":...}
// and finally to a bare status line.
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
	return "transcription HTTP " + strconv.Itoa(status)
}

// EncodeWavPCM16 wraps raw s16le little-endian PCM bytes in a WAV container.
// The samples are copied verbatim; only the 44-byte header is generated.
func EncodeWavPCM16(pcm []byte, sampleRate, channels int) []byte {
	dataSize := len(pcm)
	byteRate := sampleRate * channels * 2
	blockAlign := channels * 2

	buf := make([]byte, 0, 44+dataSize)
	buf = append(buf, 'R', 'I', 'F', 'F')
	buf = binary.LittleEndian.AppendUint32(buf, uint32(36+dataSize))
	buf = append(buf, 'W', 'A', 'V', 'E')
	buf = append(buf, 'f', 'm', 't', ' ')
	buf = binary.LittleEndian.AppendUint32(buf, 16) // fmt chunk size
	buf = binary.LittleEndian.AppendUint16(buf, 1)  // PCM
	buf = binary.LittleEndian.AppendUint16(buf, uint16(channels))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(sampleRate))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(byteRate))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(blockAlign))
	buf = binary.LittleEndian.AppendUint16(buf, 16) // bits per sample
	buf = append(buf, 'd', 'a', 't', 'a')
	buf = binary.LittleEndian.AppendUint32(buf, uint32(dataSize))
	return append(buf, pcm...)
}
