package outbound

import (
	"context"
)

// PollRepository is the Right Gate. The engine demands these functions exist; Postgres will eventually fulfill them.
type PollRepository interface {
	AttemptAtomicLock(ctx context.Context, optionID string, userID string) error
}
