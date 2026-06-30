package domain

import "time"

// Poll is the aggregate root — a poll plus all its options.
type Poll struct {
	ID        string
	Title     string
	CreatedBy string
	ChannelID string
	MessageID string
	ExpiresAt time.Time
	Options   []PollOption
}
