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
	// ── Signature verification ──
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
	case 1:
		w.Write([]byte(`{"type":1}`))
	case 2:
		w.Write(buildModalJSON())
	case 5:
		h.handleModalSubmit(r.Context(), w, rawBody, p.Member.User.ID, p.ChannelID, p.Token)
	case 3:
		w.Write([]byte(`{"type":6}`))
		h.spawn(func() {
			h.handleClaim(context.Background(), p.Data.CustomID, p.Member.User.ID, p.ChannelID, p.Message.ID, p.Token)
		})
	default:
		http.Error(w, "unknown interaction type", http.StatusBadRequest)
	}
}

func (h *Handler) handleClaim(ctx context.Context, optionID, userID, channelID, messageID, token string) {
	err := h.engine.ClaimOption(ctx, inbound.ClaimOptionCommand{
		OptionID:  optionID,
		UserID:    userID,
		Platform:  "discord",
		MessageID: messageID,
		ChannelID: channelID,
	})
	if err != nil {
		log.Printf("[DISCORD] Claim rejected: %v", err)
		// Fire a ghost message only the user can see
		h.sendEphemeralWarning(token, "❌ **Action Denied.** You either already hold an option in this poll, or someone else locked this one first. Release yours to switch!")
		return
	}
	h.refreshPollMessage(ctx, optionID, channelID, messageID)
}

func (h *Handler) sendEphemeralWarning(token, msg string) {
	url := fmt.Sprintf("https://discord.com/api/v10/webhooks/%s/%s", h.appID, token)
	payload := map[string]interface{}{
		"content": msg,
		"flags":   64, // 64 is the physical flag for an Ephemeral message
	}
	jsonData, _ := json.Marshal(payload)
	http.Post(url, "application/json", bytes.NewBuffer(jsonData))
}

func (h *Handler) handleModalSubmit(ctx context.Context, w http.ResponseWriter, rawBody []byte, userID, channelID, token string) {
	var modal struct {
		Data struct {
			Components []struct {
				Components []struct {
					CustomID string `json:"custom_id"`
					Value    string `json:"value"`
				} `json:"components"`
			} `json:"components"`
		} `json:"data"`
	}
	json.Unmarshal(rawBody, &modal)

	question, optionsRaw := "", ""
	rawDuration := "24h" // Fallback default string

	for _, row := range modal.Data.Components {
		for _, field := range row.Components {
			if field.CustomID == "input_question" {
				question = field.Value
			} else if field.CustomID == "input_options" {
				optionsRaw = field.Value
			} else if field.CustomID == "input_duration" && field.Value != "" {
				rawDuration = field.Value // Capture the raw string (e.g., "1h30m")
			}
		}
	}

	options := parseOptions(optionsRaw)
	switch {
	case len(options) < 2:
		w.Write([]byte(`{"type":4,"data":{"content":"❌ Need at least 2 options.","flags":64}}`))
		return
	case len(options) > 25:
		w.Write([]byte(`{"type":4,"data":{"content":"❌ Max 25 options.","flags":64}}`))
		return
	}

	// Natively parse the complex time string into exact nanoseconds
	duration, err := time.ParseDuration(rawDuration)
	if err != nil {
		duration = 24 * time.Hour // Fallback if they type garbage
	}

	// Defensive limits: minimum 1 minute, maximum 168 hours (1 week)
	if duration < time.Minute {
		duration = time.Minute
	} else if duration > 168*time.Hour {
		duration = 168 * time.Hour
	}

	// Pass the new Duration field to the engine
	result, err := h.engine.CreatePoll(ctx, inbound.CreatePollCommand{
		Question:  question,
		Options:   options,
		CreatedBy: userID,
		ChannelID: channelID,
		Duration:  duration, // This now correctly matches your updated struct
	})
	if err != nil {
		w.Write([]byte(`{"type":4,"data":{"content":"❌ Failed to create poll.","flags":64}}`))
		return
	}

	// Calculate the future Unix timestamp for Discord's native ticking UI clock
	expiresAtUnix := time.Now().Add(duration).Unix()

	response := map[string]interface{}{
		"type": 4,
		"data": map[string]interface{}{
			// The <t:UNIX:R> tag forces Discord to render a live, ticking countdown on the user's screen
			"content":    fmt.Sprintf("📊 **%s**\n> *First come, first served. Ends <t:%d:R>.*", question, expiresAtUnix),
			"components": buildFreeButtonRow(result.Options),
		},
	}
	json.NewEncoder(w).Encode(response)

	h.spawn(func() { h.anchorPollToMessage(context.Background(), result.PollID, token) })
}
func (h *Handler) anchorPollToMessage(ctx context.Context, pollID, token string) {
	url := fmt.Sprintf("https://discord.com/api/v10/webhooks/%s/%s/messages/@original", h.appID, token)
	client := &http.Client{Timeout: 5 * time.Second}

	backoff := 500 * time.Millisecond
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(backoff)
		resp, err := client.Get(url)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			backoff *= 2
			continue
		}

		var msg struct {
			ID string `json:"id"`
		}
		json.NewDecoder(resp.Body).Decode(&msg)
		resp.Body.Close()

		if msg.ID != "" {
			h.engine.UpdatePollMessage(ctx, pollID, msg.ID)
			return
		}
	}
}

// refreshPollMessage uses a per-message mutex lock to prevent concurrent state tearing
func (h *Handler) refreshPollMessage(ctx context.Context, optionID, channelID, messageID string) {
	mu, _ := h.patchLocks.LoadOrStore(messageID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	poll, err := h.engine.GetPollByOptionID(ctx, optionID)
	if err != nil {
		return
	}

	patchBody := map[string]interface{}{"components": buildUpdatedButtonRow(poll.Options)}
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
			return
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			var rl struct {
				RetryAfter float64 `json:"retry_after"`
			}
			json.NewDecoder(resp.Body).Decode(&rl)
			resp.Body.Close()
			time.Sleep(time.Duration(rl.RetryAfter * float64(time.Second)))
			continue
		}
		resp.Body.Close()
		return
	}
}

// ── Component Builders (Chunking Logic) ──

func buildModalJSON() []byte {
	return []byte(`{
        "type": 9,
        "data": {
            "title": "Create a BidPoll",
            "custom_id": "modal_create_poll",
            "components": [
                {"type":1,"components":[{"type":4,"custom_id":"input_question","label":"Question","style":1,"min_length":5,"max_length":100,"required":true,"placeholder":"Who wins the championship?"}]},
                {"type":1,"components":[{"type":4,"custom_id":"input_options","label":"Options (one per line, max 25)","style":2,"min_length":3,"max_length":500,"required":true,"placeholder":"Batman\nSuperman\nWonder Woman"}]},
                {"type":1,"components":[{"type":4,"custom_id":"input_duration","label":"Duration (e.g., 2h30m, 90m, 1h)","style":1,"min_length":2,"max_length":10,"required":false,"placeholder":"24h"}]}
            ]
        }
    }`)
}

func buildFreeButtonRow(options []inbound.OptionView) []map[string]interface{} {
	var rows []map[string]interface{}
	var currentRow []interface{}

	for i, opt := range options {
		currentRow = append(currentRow, map[string]interface{}{
			"type": 2, "label": truncate(opt.Text, 80), "style": 1, "custom_id": opt.ID,
		})
		if len(currentRow) == 5 || i == len(options)-1 {
			rows = append(rows, map[string]interface{}{"type": 1, "components": currentRow})
			currentRow = nil
		}
	}
	return rows
}

func buildUpdatedButtonRow(options []inbound.OptionView) []map[string]interface{} {
	var rows []map[string]interface{}
	var currentRow []interface{}

	for i, opt := range options {
		style := 1
		label := truncate(opt.Text, 80)

		if opt.State == "LOCKED" {
			style = 2
			label = truncate("🔒 "+opt.Text, 80)
		}

		currentRow = append(currentRow, map[string]interface{}{
			"type": 2, "label": label, "style": style, "custom_id": opt.ID,
		})

		if len(currentRow) == 5 || i == len(options)-1 {
			rows = append(rows, map[string]interface{}{"type": 1, "components": currentRow})
			currentRow = nil
		}
	}
	return rows
}

func parseOptions(raw string) []string {
	var out []string
	for _, l := range strings.Split(raw, "\n") {
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
