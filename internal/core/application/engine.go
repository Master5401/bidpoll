package application

import (
	"context"
	"fmt"
	"time"

	"github.com/shakunth/bidpoll/internal/core/domain"
	"github.com/shakunth/bidpoll/internal/ports/inbound"
	"github.com/shakunth/bidpoll/internal/ports/outbound"
)

type PollEngine struct {
	repo     outbound.PollRepository
	eventBus outbound.EventBus
}

func NewPollEngine(repo outbound.PollRepository, bus outbound.EventBus) *PollEngine {
	return &PollEngine{repo: repo, eventBus: bus}
}

func (e *PollEngine) ClaimOption(ctx context.Context, cmd inbound.ClaimOptionCommand) error {
	if err := e.repo.AttemptAtomicLock(ctx, cmd.OptionID, cmd.UserID); err != nil {
		return fmt.Errorf("claim failed: %w", err)
	}
	return e.eventBus.Publish(ctx, domain.PollEvent{
		Type:      domain.EvtOptionClaimed,
		PollID:    cmd.PollID,
		OptionID:  cmd.OptionID,
		UserID:    cmd.UserID,
		Platform:  cmd.Platform,
		MessageID: cmd.MessageID,
		Timestamp: time.Now(),
	})
}

func (e *PollEngine) CreatePoll(ctx context.Context, cmd inbound.CreatePollCommand) (*inbound.CreatePollResult, error) {
	pollID, err := e.repo.CreatePoll(ctx, cmd.Question, cmd.CreatedBy, cmd.ChannelID, cmd.Duration)
	if err != nil {
		return nil, fmt.Errorf("failed to create poll: %w", err)
	}
	result := &inbound.CreatePollResult{PollID: pollID}
	for _, text := range cmd.Options {
		optID, err := e.repo.AddOption(ctx, pollID, text)
		if err != nil {
			return nil, fmt.Errorf("failed to add option '%s': %w", text, err)
		}
		result.Options = append(result.Options, inbound.OptionView{
			ID:    optID,
			Text:  text,
			State: string(domain.StateFree),
		})
	}
	return result, nil
}

func (e *PollEngine) UpdatePollMessage(ctx context.Context, pollID, messageID string) error {
	return e.repo.UpdatePollMessage(ctx, pollID, messageID)
}

func (e *PollEngine) GetPollByOptionID(ctx context.Context, optionID string) (*inbound.PollView, error) {
	poll, err := e.repo.GetPollWithOptions(ctx, optionID)
	if err != nil {
		return nil, err
	}
	view := &inbound.PollView{
		ID:        poll.ID,
		ChannelID: poll.ChannelID,
		MessageID: poll.MessageID,
	}
	for _, opt := range poll.Options {
		view.Options = append(view.Options, inbound.OptionView{
			ID:     opt.ID,
			Text:   opt.Text,
			State:  string(opt.State),
			HeldBy: opt.HeldBy,
		})
	}
	return view, nil
}

// Compile-time proof the engine satisfies both sides.
var _ inbound.PollUseCase = (*PollEngine)(nil)
