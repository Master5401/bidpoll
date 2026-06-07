package inbound

import "context"

// ClaimOptionCommand is a data transfer object. It moves data from the outside world into the engine.
type ClaimOptionCommand struct {
	PollID    string
	OptionID  string
	UserID    string
	Platform  string
	MessageID string
}

// PollUseCase is the Left Gate. Discord and Slack MUST use this interface to talk to the Engine.
type PollUseCase interface {
	ClaimOption(ctx context.Context, cmd ClaimOptionCommand) error
}
