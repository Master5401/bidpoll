package application

import (
	"context"
	"fmt"
	"time"

	// Adjust these imports to match your actual go.mod module name
	"github.com/shakunth/bidpoll/internal/core/domain"
	"github.com/shakunth/bidpoll/internal/ports/inbound"
	"github.com/shakunth/bidpoll/internal/ports/outbound"
)

// PollEngine is the absolute center of the Hexagon.
type PollEngine struct {
	repo     outbound.PollRepository // The Database Gate
	eventBus outbound.EventBus       // The Broadcaster Gate
}

// NewPollEngine injects the interfaces, NOT the implementations.
func NewPollEngine(repo outbound.PollRepository, bus outbound.EventBus) *PollEngine {
	return &PollEngine{
		repo:     repo,
		eventBus: bus,
	}
}

// ClaimOption explicitly satisfies the inbound.PollUseCase interface.
func (e *PollEngine) ClaimOption(ctx context.Context, cmd inbound.ClaimOptionCommand) error {

	// STEP 1: The Atomic Lock (Delegating to Postgres)
	// We pass the raw data from the command into the repository.
	err := e.repo.AttemptAtomicLock(ctx, cmd.OptionID, cmd.UserID)
	if err != nil {
		// If rows affected == 0, Postgres rejected it.
		// We wrap the error so the HTTP handler knows EXACTLY what failed.
		return fmt.Errorf("claim option failed: %w", err)
	}

	// STEP 2: The Broadcast (The Observer Pattern)
	// The database lock is committed and safe. Now we scream the fact into the void.
	// The engine DOES NOT CARE if Discord is offline. Its job is done.
	evt := domain.PollEvent{
		Type:      domain.EvtOptionClaimed,
		PollID:    cmd.PollID,
		OptionID:  cmd.OptionID,
		UserID:    cmd.UserID,
		Platform:  cmd.Platform,
		MessageID: cmd.MessageID,
		Timestamp: time.Now(),
	}

	// We publish the event and immediately return.
	// The EventBus adapter will handle routing this to Discord in a background Goroutine.
	return e.eventBus.Publish(ctx, evt)
}

// Compile-Time Check: This line mathematically proves the Engine satisfies the interface.
// If you change the interface but forget to update the Engine, the compiler crashes right here.
var _ inbound.PollUseCase = (*PollEngine)(nil)
