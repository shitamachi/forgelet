package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// IngestPort is the consumer-side ingestion port satisfied by the 0002
// scheduler Ingestor.
type IngestPort interface {
	Ingest(ctx context.Context, d model.Delivery) (model.RunID, bool, error)
}

// MaxWebhookBody bounds webhook payload reads (GitHub payloads are small).
const MaxWebhookBody = 10 << 20 // 10 MiB

// WebhookHandler serves POST /webhooks/github. It verifies the signature
// before any durable write and deduplicates deliveries through the ingest
// port (0002 FR-B).
type WebhookHandler struct {
	secret []byte
	ingest IngestPort
}

// NewWebhookHandler wires the handler. secret is the GitHub App webhook
// secret.
func NewWebhookHandler(secret []byte, ingest IngestPort) *WebhookHandler {
	return &WebhookHandler{secret: append([]byte(nil), secret...), ingest: ingest}
}

// ServeHTTP implements http.Handler.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxWebhookBody))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if err := VerifySignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		// Signature failures never reach durable storage and never retry.
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}

	deliveryID := r.Header.Get("X-GitHub-Delivery")
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	key := model.DeliveryKey{Provider: "github", DeliveryID: deliveryID}

	switch event {
	case "push":
		ev, err := DecodePush(body, deliveryID)
		switch {
		case errors.Is(err, ErrIgnoredPush):
			writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "reason": "branch deleted"})
			return
		case err != nil:
			http.Error(w, "malformed push payload", http.StatusBadRequest)
			return
		}
		runID, created, err := h.ingest.Ingest(r.Context(), model.Delivery{Key: key, Event: ev, Payload: body})
		if err != nil {
			http.Error(w, "ingest failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"runId": string(runID), "created": created})
	default:
		// Unknown/unhandled event types are acknowledged (no GitHub retry
		// storm); FR-G1.4 keeps a durable receipt without compiling.
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true, "event": event})
	}
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The header is already sent; a marshal failure can only be logged by
	// the outer middleware, so the error is dropped here.
	_ = json.NewEncoder(w).Encode(body)
}
