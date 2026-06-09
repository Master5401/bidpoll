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

func (h *Handler) refreshPollMessage(ctx context.Context, optionID, channelID, messageID string) {
	// All PATCH goroutines for this message queue here.
	// Each one reads DB state INSIDE the lock, so the last one out sees all claims.
	mu, _ := h.patchLocks.LoadOrStore(messageID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	poll, err := h.engine.GetPollByOptionID(ctx, optionID)
	if err != nil {
		log.Printf("[DISCORD] GetPollByOptionID failed: %v", err)
		return
	}

	patchBody := map[string]interface{}{"components": buildUpdatedButtonRow(poll.Options)}
	jsonData, _ := json.Marshal(patchBody)
	url := fmt.Sprintf("https://discord.com/api/v10/channels/%s/messages/%s", channelID, messageID)

	h.patchWithRateLimit(url, jsonData)
}

// patchWithRateLimit handles Discord's 429 responses using the retry_after they send back.
// Never hardcode a sleep time when the server tells you exactly how long to wait.
func (h *Handler) patchWithRateLimit(url string, jsonData []byte) {
	for {
		req, _ := http.NewRequest("PATCH", url, bytes.NewBuffer(jsonData))
		req.Header.Set("Authorization", "Bot "+h.botToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			log.Printf("[DISCORD] PATCH network error: %v", err)
			return
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			var rl struct {
				RetryAfter float64 `json:"retry_after"`
			}
			json.NewDecoder(resp.Body).Decode(&rl)
			resp.Body.Close()
			wait := time.Duration(rl.RetryAfter * float64(time.Second))
			log.Printf("[DISCORD] Rate limited. Respecting retry_after: %v", wait)
			time.Sleep(wait)
			continue
		}

		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("[DISCORD] PATCH returned %d", resp.StatusCode)
		}
		return
	}
}
