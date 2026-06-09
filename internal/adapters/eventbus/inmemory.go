package eventbus

import (
	"context"
	"fmt"
	"sync"

	// Adjust these to match your actual go.mod module name
	"github.com/shakunth/bidpoll/internal/core/domain"
	"github.com/shakunth/bidpoll/internal/ports/outbound"
)

// InMemoryEventBus acts as the local traffic router.
type InMemoryEventBus struct {
	// CRITICAL FIX: The value is now a SLICE of handlers.
	handlers map[domain.EventType][]outbound.EventHandler
	mutex    sync.RWMutex
}

// NewInMemoryEventBus is the constructor. You must initialize maps in Go before using them.
func NewInMemoryEventBus() *InMemoryEventBus {
	return &InMemoryEventBus{
		handlers: make(map[domain.EventType][]outbound.EventHandler),
	}
}

// Subscribe attaches a new listener (like Discord or Slack) to a specific event type.
func (b *InMemoryEventBus) Subscribe(eventType domain.EventType, handler outbound.EventHandler) {
	// THE PHYSICS: We are physically modifying the map.
	// We need an absolute write lock so two servers booting up don't crash the map.
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// Append the new handler to the slice of existing handlers
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

// Publish screams the fact into the void for any observer listening.
func (b *InMemoryEventBus) Publish(ctx context.Context, event domain.PollEvent) error {
	// THE PHYSICS: We are ONLY reading the map.
	// We use RLock (Read Lock) so 50 concurrent claims can read this map at the exact same millisecond.
	b.mutex.RLock()

	// We copy the slice of handlers locally, and immediately release the lock.
	// If we hold the lock while the handlers are executing, we bottleneck the entire engine.
	subscribers := b.handlers[event.Type]
	b.mutex.RUnlock()

	// Loop through every subscribed observer and execute their logic.
	for _, handler := range subscribers {

		go func(h outbound.EventHandler) {
			err := h(ctx, event)
			if err != nil {
				// The DB lock is secure, but the UI failed.
				// In a real system, you push this to a structured logger (like Zap) or a retry queue.
				fmt.Printf("[EventBus Error] observer failed to process %s for option %s: %v\n", event.Type, event.OptionID, err)
			}
		}(handler)

	}

	return nil
}
