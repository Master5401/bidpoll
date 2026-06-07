package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/shakunth/bidpoll/internal/core/domain"
	"github.com/shakunth/bidpoll/internal/ports/outbound"
)

// PollRepo is the physical machinery that talks to the database.
type PollRepo struct {
	db *sql.DB
}

// NewPollRepo is the constructor. It requires a live database connection pool.
func NewPollRepo(db *sql.DB) *PollRepo {
	return &PollRepo{db: db}
}

// AttemptAtomicLock executes the single-query optimistic lock.
func (r *PollRepo) AttemptAtomicLock(ctx context.Context, optionID string, userID string) error {
	// 1. The raw SQL string using positional parameters. No stray quotes.
	query := `
		UPDATE poll_options 
		SET state = 'LOCKED', 
		    held_by = $1, 
		    locked_at = NOW() 
		WHERE id = $2 
		  AND state = 'FREE'
	`

	// 2. Fire the query through the connection pool.
	result, err := r.db.ExecContext(ctx, query, userID, optionID)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}

	// 3. Extract the integer telling you how many rows actually changed.
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	// 4. Enforce the physics of the domain.
	if rows == 0 {
		return domain.ErrOptionAlreadyClaimed
	}

	return nil
}

// Compile-Time Verification: Proves this struct satisfies the interface.
var _ outbound.PollRepository = (*PollRepo)(nil)
