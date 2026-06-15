package outbound

import (
	"context"
	"time"

	"github.com/shakunth/bidpoll/internal/core/domain"
)

// PollRepository is the Right Gate.
// The engine demands these; the Postgres adapter fulfills them.
type PollRepository interface {
	AttemptAtomicLock(ctx context.Context, optionID, userID string) error
	CreatePoll(ctx context.Context, title, createdBy, channelID string, expiresAt time.Time) (string, error)
	AddOption(ctx context.Context, pollID, text string) (string, error)
	UpdatePollMessage(ctx context.Context, pollID, messageID string) error
	GetPollWithOptions(ctx context.Context, optionID string) (*domain.Poll, error)
}
