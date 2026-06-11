package discord

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shakunth/bidpoll/internal/ports/inbound"
)

// Handler is the Discord inbound adapter.
type Handler struct {
	engine     inbound.PollUseCase
	pubKey     ed25519.PublicKey
	appID      string
	botToken   string
	wg         sync.WaitGroup
	patchLocks sync.Map
}

func (h *Handler) spawn(fn func()) {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		fn()
	}()
}

// Drain blocks until every in-flight operation completes.
// Call this after the HTTP server stops accepting new requests.
func (h *Handler) Drain() {
	h.wg.Wait()
}

func NewHandler(engine inbound.PollUseCase, pubKeyHex, appID, botToken string) *Handler {
	keyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(keyBytes) != ed25519.PublicKeySize {
		log.Fatalf("[DISCORD FATAL] Invalid DISCORD_PUBLIC_KEY")
	}
	return &Handler{
		engine:   engine,
		pubKey:   ed25519.PublicKey(keyBytes),
		appID:    appID,
		botToken: botToken,
	}
}

func RegisterSlashCommands(appID, botToken string) error {
	url := fmt.Sprintf("https://discord.com/api/v10/applications/%s/commands", appID)
	cmdBody := map[string]interface{}{
		"name":        "create-poll",
		"description": "Create a BidPoll — one exclusive claimer per option",
		"type":        1,
	}
	jsonData, _ := json.Marshal(cmdBody)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord rejected command: %d — %s", resp.StatusCode, b)
	}
	return nil
}

func (h *Handler) HandleInteraction(w http.ResponseWriter, r *http.Request) {
	// ── Signature verification ────────────────────────────────────────────────
	sigHex := r.Header.Get("X-Signature-Ed25519")
	ts := r.Header.Get("X-Signature-Timestamp")
	if sigHex == "" || ts == "" {
		http.Error(w, "missing signature headers", http.StatusUnauthorized)
		return
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		http.Error(w, "invalid signature format", http.StatusUnauthorized)
		return
	}
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusInternalServerError)
		return
	}
	if !ed25519.Verify(h.pubKey, append([]byte(ts), rawBody...), sig) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// ── Decode top-level fields ───────────────────────────────────────────────
	var p struct {
		Type      int    `json:"type"`
		ChannelID string `json:"channel_id"`
		Token     string `json:"token"`
		Data      struct {
			CustomID string `json:"custom_id"`
		} `json:"data"`
		Member struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"member"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if err := json.Unmarshal(rawBody, &p); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch p.Type {
	case 1: // PING
		w.Write([]byte(`{"type":1}`))

	case 2: // SLASH COMMAND → fire the modal
		w.Write(buildModalJSON())

	case 5: // MODAL_SUBMIT
		h.handleModalSubmit(r.Context(), w, rawBody, p.Member.User.ID, p.ChannelID, p.Token)

	case 3: // BUTTON CLICK
		// Respond immediately — silent ACK, no visible effect on the user
		w.Write([]byte(`{"type":6}`))
		h.spawn(func() {
			h.handleClaim(
				context.Background(),
				p.Data.CustomID, // = option UUID stored as button custom_id
				p.Member.User.ID,
				p.ChannelID,
				p.Message.ID,
			)
		})

	default:
		http.Error(w, "unknown interaction type", http.StatusBadRequest)
	}
}

// handleClaim: DB write + message refresh, runs in a goroutine.
func (h *Handler) handleClaim(ctx context.Context, optionID, userID, channelID, messageID string) {
	err := h.engine.ClaimOption(ctx, inbound.ClaimOptionCommand{
		OptionID:  optionID,
		UserID:    userID,
		Platform:  "discord",
		MessageID: messageID,
		ChannelID: channelID,
	})
	if err != nil {
		log.Printf("[DISCORD] Claim rejected — option %s by user %s: %v", optionID, userID, err)
		return // Option already taken; original message stays unchanged, no action needed
	}
	h.refreshPollMessage(ctx, optionID, channelID, messageID)
}

// handleModalSubmit: parse form values, create poll, send message.
func (h *Handler) handleModalSubmit(
	ctx context.Context, w http.ResponseWriter,
	rawBody []byte, userID, channelID, token string,
) {
	var modal struct {
		Data struct {
			Components []struct {
				Type       int `json:"type"`
				Components []struct {
					CustomID string `json:"custom_id"`
					Value    string `json:"value"`
				} `json:"components"`
			} `json:"components"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &modal); err != nil {
		http.Error(w, "invalid modal payload", http.StatusBadRequest)
		return
	}

	question, optionsRaw := "", ""
	for _, row := range modal.Data.Components {
		for _, field := range row.Components {
			switch field.CustomID {
			case "input_question":
				question = field.Value
			case "input_options":
				optionsRaw = field.Value
			}
		}
	}

	options := parseOptions(optionsRaw)
	switch {
	case len(options) < 2:
		w.Write([]byte(`{"type":4,"data":{"content":"❌ Need at least 2 options.","flags":64}}`))
		return
	case len(options) > 5:
		w.Write([]byte(`{"type":4,"data":{"content":"❌ Max 5 options (Discord button row limit).","flags":64}}`))
		return
	}

	result, err := h.engine.CreatePoll(ctx, inbound.CreatePollCommand{
		Question:  question,
		Options:   options,
		CreatedBy: userID,
		ChannelID: channelID,
	})
	if err != nil {
		log.Printf("[DISCORD] CreatePoll failed: %v", err)
		w.Write([]byte(`{"type":4,"data":{"content":"❌ Failed to create poll. Try again.","flags":64}}`))
		return
	}

	// Post the poll message to the channel via interaction response
	response := map[string]interface{}{
		"type": 4,
		"data": map[string]interface{}{
			"content":    fmt.Sprintf("📊 **%s**\n> *First come, first served — claim your pick below. Only you can release your claim.*", question),
			"components": buildFreeButtonRow(result.Options),
		},
	}
	json.NewEncoder(w).Encode(response)

	// Async: retrieve the posted message ID and store it in DB
	h.spawn(func() { h.anchorPollToMessage(context.Background(), result.PollID, token) })
}

// anchorPollToMessage fetches the interaction response message and persists its ID.
func (h *Handler) anchorPollToMessage(ctx context.Context, pollID, token string) {
	url := fmt.Sprintf("https://discord.com/api/v10/webhooks/%s/%s/messages/@original", h.appID, token)
	client := &http.Client{Timeout: 5 * time.Second}

	backoff := 500 * time.Millisecond
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(backoff)

		resp, err := client.Get(url)
		if err != nil {
			log.Printf("[DISCORD] Anchor attempt %d — network error: %v", attempt, err)
			backoff *= 2
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			log.Printf("[DISCORD] Anchor attempt %d — got HTTP %d", attempt, resp.StatusCode)
			backoff *= 2
			continue
		}

		var msg struct {
			ID string `json:"id"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&msg)
		resp.Body.Close() // explicit — no defer inside a loop

		if decodeErr != nil || msg.ID == "" {
			log.Printf("[DISCORD] Anchor attempt %d — decode failed or empty ID", attempt)
			backoff *= 2
			continue
		}

		if err := h.engine.UpdatePollMessage(ctx, pollID, msg.ID); err != nil {
			log.Printf("[DISCORD] Anchor DB write failed for poll %s: %v", pollID, err)
		} else {
			log.Printf("[DISCORD] Poll %s anchored to message %s (attempt %d)", pollID, msg.ID, attempt)
		}
		return
	}
	log.Printf("[DISCORD] Poll %s orphaned — all 3 anchor attempts exhausted", pollID)
}

// refreshPollMessage rebuilds the button layout and PATCHes the original poll message.
// refreshPollMessage rebuilds the button layout and PATCHes the original poll message.
func (h *Handler) refreshPollMessage(ctx context.Context, optionID, channelID, messageID string) {
	// CRITICAL: All PATCH goroutines for this message queue here.
	// Each one reads DB state INSIDE the lock, so the last one out sees all claims perfectly.
	mu, _ := h.patchLocks.LoadOrStore(messageID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	poll, err := h.engine.GetPollByOptionID(ctx, optionID)
	if err != nil {
		log.Printf("[DISCORD] GetPollByOptionID failed: %v", err)
		return
	}

	patchBody := map[string]interface{}{
		"components": buildUpdatedButtonRow(poll.Options),
	}
	jsonData, _ := json.Marshal(patchBody)

	url := fmt.Sprintf("https://discord.com/api/v10/channels/%s/messages/%s", channelID, messageID)
	h.patchWithRateLimit(url, jsonData)
}

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

// ── Component Builders ────────────────────────────────────────────────────────

func buildModalJSON() []byte {
	return []byte(`{
        "type": 9,
        "data": {
            "title": "Create a BidPoll",
            "custom_id": "modal_create_poll",
            "components": [
                {"type":1,"components":[{
                    "type":4,"custom_id":"input_question",
                    "label":"Question","style":1,
                    "min_length":5,"max_length":100,"required":true,
                    "placeholder":"Who wins the championship?"
                }]},
                {"type":1,"components":[{
                    "type":4,"custom_id":"input_options",
                    "label":"Options (one per line, max 5)","style":2,
                    "min_length":3,"max_length":500,"required":true,
                    "placeholder":"Batman\nSuperman\nWonder Woman"
                }]}
            ]
        }
    }`)
}

func buildFreeButtonRow(options []inbound.OptionView) []map[string]interface{} {
	var rows []map[string]interface{}

	for _, opt := range options {
		// Build the individual button
		button := map[string]interface{}{
			"type":      2,
			"label":     truncate(opt.Text, 80),
			"style":     1, // PRIMARY — blurple
			"custom_id": opt.ID,
		}

		// Wrap THIS single button in its own Action Row, forcing a new line
		actionRow := map[string]interface{}{
			"type":       1,
			"components": []interface{}{button},
		}
		rows = append(rows, actionRow)
	}
	return rows
}

func buildUpdatedButtonRow(options []inbound.OptionView) []map[string]interface{} {
	var rows []map[string]interface{}

	for _, opt := range options {
		var button map[string]interface{}

		if opt.State == "LOCKED" {
			button = map[string]interface{}{
				"type":      2,
				"label":     truncate("🔒 "+opt.Text, 80),
				"style":     2, // SECONDARY — grey
				"custom_id": opt.ID,
				"disabled":  true,
			}
		} else {
			button = map[string]interface{}{
				"type":      2,
				"label":     truncate(opt.Text, 80),
				"style":     1,
				"custom_id": opt.ID,
			}
		}

		// Wrap THIS single button in its own Action Row
		actionRow := map[string]interface{}{
			"type":       1,
			"components": []interface{}{button},
		}
		rows = append(rows, actionRow)
	}
	return rows
}

// ── Utilities ─────────────────────────────────────────────────────────────────

func parseOptions(raw string) []string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}
