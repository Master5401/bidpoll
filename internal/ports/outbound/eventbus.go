package outbound

import (
	"context"

	// Adjust to your go.mod
	"github.com/shakunth/bidpoll/internal/core/domain"
)

type EventHandler func(ctx context.Context, event domain.PollEvent) error

// EventBus is the loudspeaker contract.
// Any adapter that wants to act as a router must sign this.
type EventBus interface {
	Publish(ctx context.Context, event domain.PollEvent) error
}
