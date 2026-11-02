package scrapers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchJSON_InvalidJSONWrapsSyntaxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{oops"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "example.com"}}
	_, err := fetchJSON(context.Background(), client, "https://example.com/data")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if want := `invalid JSON from https://example.com/data: invalid character 'o' looking for beginning of object key string`; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("error %v does not unwrap to *json.SyntaxError", err)
	}
}

func TestFetchBytes_SetsIdentifyingHeaders(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "example.com"}}
	if _, err := fetchBytes(context.Background(), client, "https://example.com/x"); err != nil {
		t.Fatalf("fetchBytes() error = %v", err)
	}
	if gotUA != "gbot/1.0" {
		t.Errorf("User-Agent = %q, want gbot/1.0", gotUA)
	}
	if gotAccept != "application/json, text/html" {
		t.Errorf("Accept = %q, want application/json, text/html", gotAccept)
	}
}

func TestFetchBytes_HTTPErrorIncludesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "example.com"}}
	_, err := fetchBytes(context.Background(), client, "https://example.com/nope")
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if err.Error() != "HTTP 403 from https://example.com/nope" {
		t.Errorf("error = %q, want %q", err.Error(), "HTTP 403 from https://example.com/nope")
	}
}

// The 10 MiB read cap is only observable on either side of the boundary: a
// body just under it must survive whole, a body just over it must be cut.
func TestFetchBytes_ReadCapBoundaries(t *testing.T) {
	const cap = 10 << 20
	tests := []struct {
		name string
		size int
		want int
	}{
		{"under_cap_keeps_everything", cap - 500_000, cap - 500_000},
		{"over_cap_truncates", cap + 1_500_000, cap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, strings.Repeat("a", tt.size))
			}))
			defer srv.Close()

			client := &http.Client{Transport: &rewriteTransport{server: srv, host: "example.com"}}
			data, err := fetchBytes(context.Background(), client, "https://example.com/big")
			if err != nil {
				t.Fatalf("fetchBytes() error = %v", err)
			}
			if len(data) != tt.want {
				t.Errorf("len(data) = %d, want %d", len(data), tt.want)
			}
		})
	}
}

type truncatedBody struct{ sent bool }

func (b *truncatedBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.ErrUnexpectedEOF
	}
	b.sent = true
	copy(p, "partial")
	return 7, nil
}

func (b *truncatedBody) Close() error { return nil }

func TestFetchBytes_BodyReadErrorPropagates(t *testing.T) {
	client := &http.Client{Transport: fakeTransport{resp: &http.Response{
		StatusCode:    http.StatusOK,
		Body:          &truncatedBody{},
		Header:        http.Header{},
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		ContentLength: -1,
	}}}
	data, err := fetchBytes(context.Background(), client, "https://example.com/truncated")
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
	}
	if data != nil {
		t.Errorf("data = %q, want nil on read error", string(data))
	}
}

func TestFetchBytes_TransportErrorPropagates(t *testing.T) {
	sentinel := errors.New("dial refused")
	client := &http.Client{Transport: errTransport{err: sentinel}}
	_, err := fetchBytes(context.Background(), client, "https://example.com/x")
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want wrapped %v", err, sentinel)
	}
}
