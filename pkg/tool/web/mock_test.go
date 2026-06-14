package web

import (
	"context"

	"github.com/liuy/gbot/pkg/tool/web/providers"
)

type mockProvider struct {
	id        string
	available bool
	resp      *providers.SearchResponse
	err       error
}

func (m *mockProvider) ID() string        { return m.id }
func (m *mockProvider) IsAvailable() bool { return m.available }
func (m *mockProvider) Search(ctx context.Context, params providers.SearchParams) (*providers.SearchResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}
