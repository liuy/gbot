// Package connector defines the Connector interface that all platform
// connectors must implement. Each connector bridges a chat platform
// (WeChat, Discord, Telegram, etc.) to a gbot engine.
package connector

import (
	"context"

	"github.com/liuy/gbot/pkg/hub"
)

// Connector is the interface that all platform connectors must implement.
type Connector interface {
	// Start begins the connector's polling and processing loops.
	// ctx controls the lifecycle — cancel to stop.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the connector.
	Stop()

	// Send sends a text message to a user on the platform.
	Send(userID, text string) error

	// Handle receives engine events from Hub.Dispatch.
	Handle(event hub.Event)
}
