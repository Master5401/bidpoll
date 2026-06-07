package domain

import "errors"

// OptionState mathematically restricts the allowed states.
type OptionState string

const (
	StateFree   OptionState = "FREE"
	StateLocked OptionState = "LOCKED"
)

// Sentinel Errors act as global memory addresses for specific failures.
var (
	ErrOptionAlreadyClaimed = errors.New("option is already claimed")
	ErrNotOptionHolder      = errors.New("only the holder or an admin can release this option")
	ErrPollNotFound         = errors.New("poll not found")
)

// PollOption is an immutable snapshot of a single option.
type PollOption struct {
	ID     string
	PollID string
	Text   string
	State  OptionState
	HeldBy *string // Pointer used so we can represent a database NULL as nil
}

// CanBeClaimed is a pure function. Zero side effects.
func (o *PollOption) CanBeClaimed() error {
	if o.State == StateLocked {
		return ErrOptionAlreadyClaimed
	}
	return nil
}

// CanBeReleasedBy enforces the business rules of ownership.
func (o *PollOption) CanBeReleasedBy(userID string, isAdmin bool) error {
	if isAdmin {
		return nil
	}
	if o.HeldBy != nil && *o.HeldBy == userID {
		return nil
	}
	return ErrNotOptionHolder
}
