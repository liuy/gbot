package llm

import (
	"context"
	"errors"
)

// ErrNoProvider is the canonical degraded-mode query error: one message the
// chat surfaces verbatim, pointing at the only repair channel. Every
// NullProvider query returns this same sentinel so callers can errors.Is it.
var ErrNoProvider = errors.New("no provider configured — check settings")

// NullProvider is the tier-2/3 degraded-boot stub: boot must succeed even
// when no provider can be built, so the engine gets this stateless stub and
// every query fails with ErrNoProvider instead of the process dying.
type NullProvider struct{}

func NewNullProvider() *NullProvider { return &NullProvider{} }

func (n *NullProvider) Name() string { return "none" }

func (n *NullProvider) Complete(ctx context.Context, req *Request) (*Response, error) {
	return nil, ErrNoProvider
}

func (n *NullProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	return nil, ErrNoProvider
}
