package inbound

import "context"

// ── Commands ──────────────────────────────────────────────────────────────────

type ClaimOptionCommand struct {
	PollID    string
	OptionID  string
	UserID    string
	Platform  string
	MessageID string
	ChannelID string
}

type CreatePollCommand struct {
	Question      string
	Options       []string
	CreatedBy     string
	ChannelID     string
	DurationHours int
}

// ── Read-side DTOs ────────────────────────────────────────────────────────────

type OptionView struct {
	ID     string
	Text   string
	State  string  // "FREE" or "LOCKED"
	HeldBy *string // nil if FREE
}

type CreatePollResult struct {
	PollID  string
	Options []OptionView
}

type PollView struct {
	ID        string
	ChannelID string
	MessageID string
	Options   []OptionView
}

// ── The Left Gate ─────────────────────────────────────────────────────────────

// PollUseCase is the only contract Discord (and future Slack/Telegram adapters) may call.
type PollUseCase interface {
	ClaimOption(ctx context.Context, cmd ClaimOptionCommand) error
	CreatePoll(ctx context.Context, cmd CreatePollCommand) (*CreatePollResult, error)
	UpdatePollMessage(ctx context.Context, pollID, messageID string) error
	GetPollByOptionID(ctx context.Context, optionID string) (*PollView, error)
}
