package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/shakunth/bidpoll/internal/core/domain"
	"github.com/shakunth/bidpoll/internal/ports/outbound"
)

type PollRepo struct {
	db *sql.DB
}

func NewPollRepo(db *sql.DB) *PollRepo {
	return &PollRepo{db: db}
}

func (r *PollRepo) AttemptAtomicLock(ctx context.Context, optionID, userID string) error {
	query := `
        UPDATE poll_options
        SET 
            state = CASE WHEN state = 'FREE' THEN 'LOCKED' ELSE 'FREE' END,
            held_by = CASE WHEN state = 'FREE' THEN $1 ELSE NULL END,
            locked_at = CASE WHEN state = 'FREE' THEN NOW() ELSE NULL END
        WHERE id = $2 
        AND (
            (state = 'FREE' AND NOT EXISTS (
                SELECT 1 FROM poll_options po2 
                WHERE po2.poll_id = poll_options.poll_id 
                AND po2.held_by = $1 
                AND po2.state = 'LOCKED'
            )) 
            OR 
            (state = 'LOCKED' AND held_by = $1)
        )
    `

	result, err := r.db.ExecContext(ctx, query, userID, optionID)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected check failed: %w", err)
	}

	if rows == 0 {
		return domain.ErrOptionAlreadyClaimed
	}

	return nil
}

func (r *PollRepo) CreatePoll(ctx context.Context, title, createdBy, channelID string, expiresAt time.Time) (string, error) {
	var id string
	// Notice we replaced the NOW() math with a direct $4 injection
	query := `
        INSERT INTO polls (title, created_by, channel_id, expires_at) 
        VALUES ($1, $2, $3, $4) 
        RETURNING id
    `
	err := r.db.QueryRowContext(ctx, query, title, createdBy, channelID, expiresAt).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("failed to insert poll: %w", err)
	}
	return id, nil
}
func (r *PollRepo) AddOption(ctx context.Context, pollID, text string) (string, error) {
	var id string
	query := `INSERT INTO poll_options (poll_id, text) VALUES ($1, $2) RETURNING id`
	err := r.db.QueryRowContext(ctx, query, pollID, text).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("failed to insert option: %w", err)
	}
	return id, nil
}

func (r *PollRepo) UpdatePollMessage(ctx context.Context, pollID, messageID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE polls SET message_id = $1 WHERE id = $2`, messageID, pollID)
	return err
}

func (r *PollRepo) GetPollWithOptions(ctx context.Context, optionID string) (*domain.Poll, error) {
	// Subquery finds the parent poll for this option; then we pull all sibling options.
	query := `
        SELECT
            p.id,
            p.title,
            p.created_by,
            COALESCE(p.channel_id, '') AS channel_id,
            COALESCE(p.message_id, '') AS message_id,
            po.id       AS opt_id,
            po.text     AS opt_text,
            po.state    AS opt_state,
            po.held_by  AS opt_held_by
        FROM polls p
        JOIN poll_options po ON po.poll_id = p.id
        WHERE p.id = (SELECT poll_id FROM poll_options WHERE id = $1)
        ORDER BY po.created_at ASC
    `
	rows, err := r.db.QueryContext(ctx, query, optionID)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var poll *domain.Poll
	for rows.Next() {
		var (
			pollID, title, createdBy, channelID, messageID string
			optID, optText, optState                       string
			heldBy                                         *string
		)
		if err := rows.Scan(
			&pollID, &title, &createdBy, &channelID, &messageID,
			&optID, &optText, &optState, &heldBy,
		); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		if poll == nil {
			poll = &domain.Poll{
				ID:        pollID,
				Title:     title,
				CreatedBy: createdBy,
				ChannelID: channelID,
				MessageID: messageID,
			}
		}
		poll.Options = append(poll.Options, domain.PollOption{
			ID:     optID,
			PollID: pollID,
			Text:   optText,
			State:  domain.OptionState(optState),
			HeldBy: heldBy,
		})
	}
	if poll == nil {
		return nil, domain.ErrPollNotFound
	}
	return poll, nil
}

var _ outbound.PollRepository = (*PollRepo)(nil)
