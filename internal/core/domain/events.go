package domain

import "time"

type EventType string

const (
	EvtOptionClaimed  EventType = "OPTION_CLAIMED"
	EvtOptionReleased EventType = "OPTION_RELEASED"
)

// PollEvent is a historical fact. It represents something that already happened.
type PollEvent struct {
	Type      EventType
	PollID    string
	OptionID  string
	UserID    string
	Platform  string // e.g., "discord" or "slack"
	MessageID string
	Timestamp time.Time
}
