package llm

import (
	"context"
	"errors"
	"testing"
)

func TestNullProvider_Name(t *testing.T) {
	p := NewNullProvider()
	if p == nil {
		t.Fatal("NewNullProvider() = nil, want an instance (nil methods only work by accident of no field access)")
	}
	if got := p.Name(); got != "none" {
		t.Fatalf("Name() = %q, want %q", got, "none")
	}
}

func TestNullProvider_Complete(t *testing.T) {
	resp, err := NewNullProvider().Complete(context.Background(), &Request{Model: "any"})
	if resp != nil {
		t.Fatalf("Complete() resp = %+v, want nil", resp)
	}
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("Complete() err = %v, want ErrNoProvider", err)
	}
}

func TestNullProvider_Stream(t *testing.T) {
	ch, err := NewNullProvider().Stream(context.Background(), &Request{Model: "any"})
	if ch != nil {
		t.Fatalf("Stream() ch = %v, want nil", ch)
	}
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("Stream() err = %v, want ErrNoProvider", err)
	}
}
