package discord

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/shakunth/bidpoll/internal/ports/inbound"
)

type Handler struct {
	engine inbound.PollUseCase
	pubKey ed25519.PublicKey
}

func NewHandler(engine inbound.PollUseCase, pubKeyHex string) *Handler {
	keyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(keyBytes) != ed25519.PublicKeySize {
		// We crash on boot if the key is missing. The gate must be armed.
		log.Fatalf("[DISCORD FATAL] Invalid or missing DISCORD_PUBLIC_KEY in .env")
	}
	return &Handler{
		engine: engine,
		pubKey: ed25519.PublicKey(keyBytes),
	}
}

func (h *Handler) HandleInteraction(w http.ResponseWriter, r *http.Request) {
	// 1. Extract Cryptographic Headers
	signatureHex := r.Header.Get("X-Signature-Ed25519")
	timestamp := r.Header.Get("X-Signature-Timestamp")
	if signatureHex == "" || timestamp == "" {
		http.Error(w, "Missing signature headers", http.StatusUnauthorized)
		return
	}

	sig, err := hex.DecodeString(signatureHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		http.Error(w, "Invalid signature format", http.StatusUnauthorized)
		return
	}

	// 2. Read the raw body for verification
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusInternalServerError)
		return
	}
	// Restore body for JSON decoder later
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	// 3. Mathematical Verification (The Zero-Trust Check)
	message := []byte(timestamp)
	message = append(message, body...)
	if !ed25519.Verify(h.pubKey, message, sig) {
		log.Println("[DISCORD] Unauthorized strike rejected.")
		http.Error(w, "Invalid request signature", http.StatusUnauthorized)
		return
	}

	// 4. Decode the verified payload
	var payload struct {
		Type int `json:"type"`
		Data struct {
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

	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// 5. Handle the Discord Ping (Type 1)
	if payload.Type == 1 {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type": 1}`))
		return
	}

	// 6. Handle Button Clicks (Type 3)
	if payload.Type == 3 {
		// Translate outside world into the Hexagon
		cmd := inbound.ClaimOptionCommand{
			PollID:    "00000000-0000-0000-0000-000000000000", // Will be dynamic later
			OptionID:  payload.Data.CustomID,
			UserID:    payload.Member.User.ID,
			Platform:  "discord",
			MessageID: payload.Message.ID,
		}

		err := h.engine.ClaimOption(r.Context(), cmd)
		if err != nil {
			log.Printf("[DISCORD] Engine rejected claim: %v", err)
			w.Header().Set("Content-Type", "application/json")
			// Return ephemeral error to user
			w.Write([]byte(`{"type": 4, "data": {"content": "❌ Failed to claim option.", "flags": 64}}`))
			return
		}

		log.Println("[DISCORD] Engine accepted claim.")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type": 4, "data": {"content": "✅ Option claimed successfully!", "flags": 64}}`))
		return
	}

	http.Error(w, "Unknown Type", http.StatusBadRequest)
}
