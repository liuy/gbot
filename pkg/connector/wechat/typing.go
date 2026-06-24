package wechat

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// typingTTL is the iLink typing ticket TTL (server-side, 600s).
const typingTTL = 600 * time.Second

// typingAPI abstracts the getConfig + sendTyping API calls so tests can
// inject a mock without hitting the network.
type typingAPI interface {
	getConfig(ctx context.Context, userID, contextToken string) (ticket string, err error)
	sendTyping(ctx context.Context, userID, ticket string, status int) error
}

// iLinkTypingAPI is the production implementation of typingAPI.
type iLinkTypingAPI struct {
	client  *http.Client
	baseURL string
	token   string
}

func (a *iLinkTypingAPI) getConfig(ctx context.Context, userID, contextToken string) (string, error) {
	resp, err := GetConfig(ctx, a.client, a.baseURL, a.token, userID, contextToken)
	if err != nil {
		return "", err
	}
	return resp.TypingTicket, nil
}

func (a *iLinkTypingAPI) sendTyping(ctx context.Context, userID, ticket string, status int) error {
	return SendTyping(ctx, a.client, a.baseURL, a.token, userID, ticket, status)
}

// typingTicketCache stores typing tickets per user with TTL expiry.
// The iLink ticket has a 600s server-side TTL; once expired, both
// sendTyping(start) and sendTyping(stop) silently no-op, leaving the
// WeChat client stuck showing "typing..." forever. We proactively
// expire before that window to force a refresh.
type typingTicketCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]typingCacheEntry
}

type typingCacheEntry struct {
	ticket  string
	settled time.Time
}

func newTypingTicketCache(ttl time.Duration) *typingTicketCache {
	return &typingTicketCache{
		ttl:     ttl,
		entries: make(map[string]typingCacheEntry),
	}
}

func (c *typingTicketCache) get(userID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[userID]
	if !ok {
		return ""
	}
	if time.Since(e.settled) >= c.ttl {
		delete(c.entries, userID)
		return ""
	}
	return e.ticket
}

func (c *typingTicketCache) set(userID, ticket string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[userID] = typingCacheEntry{ticket: ticket, settled: time.Now()}
}

// ensureTicket returns a valid ticket for userID, refreshing via getConfig
// if the cached one has expired. Best-effort: returns "" on failure.
func (c *WeChatConnector) ensureTicket(ctx context.Context, userID string) string {
	if ticket := c.typingCache.get(userID); ticket != "" {
		return ticket
	}
	contextToken := c.getContextToken(userID)
	ticket, err := c.typingAPI.getConfig(ctx, userID, contextToken)
	if err != nil {
		slog.Error("wechat: getConfig failed", "user", safeID(userID), "error", err)
		return ""
	}
	if ticket != "" {
		c.typingCache.set(userID, ticket)
	}
	return ticket
}

// startTyping shows the typing indicator. Best-effort, never blocks.
func (c *WeChatConnector) startTyping(ctx context.Context, userID string) {
	if c.typingAPI == nil {
		return
	}
	ticket := c.ensureTicket(ctx, userID)
	if ticket == "" {
		return
	}
	if err := c.typingAPI.sendTyping(ctx, userID, ticket, TypingStart); err != nil {
		slog.Error("wechat: sendTyping start failed", "user", safeID(userID), "error", err)
	}
}

// stopTyping hides the typing indicator. Must refresh ticket if expired,
// otherwise the stop signal silently no-ops and the client is stuck.
func (c *WeChatConnector) stopTyping(ctx context.Context, userID string) {
	if c.typingAPI == nil {
		return
	}
	ticket := c.ensureTicket(ctx, userID)
	if ticket == "" {
		return
	}
	if err := c.typingAPI.sendTyping(ctx, userID, ticket, TypingStop); err != nil {
		slog.Error("wechat: sendTyping stop failed", "user", safeID(userID), "error", err)
	}
}
