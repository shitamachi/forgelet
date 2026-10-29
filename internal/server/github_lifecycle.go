package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
)

var errAuthorizationDuplicate = errors.New("duplicate GitHub authorization event")
var errAuthorizationDeliveryConflict = errors.New("GitHub authorization delivery content changed")

// Only a signed lifecycle hint is consumed here. Names, access rights and
// repository lists from the payload never become an authoritative catalog.
func (s *Server) githubLifecycle(w http.ResponseWriter, r *http.Request, event string, body []byte) {
	delivery := r.Header.Get("X-GitHub-Delivery")
	if !deliveryPattern.MatchString(delivery) || len(delivery) > 128 {
		http.Error(w, "invalid delivery ID", http.StatusBadRequest)
		return
	}
	var payload struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Installation.ID <= 0 || payload.Installation.ID > 9007199254740991 {
		http.Error(w, "invalid installation lifecycle payload", http.StatusBadRequest)
		return
	}
	supported := event == "installation" && slices.Contains([]string{"created", "deleted", "suspend", "unsuspend", "new_permissions_accepted"}, payload.Action) || event == "installation_repositories" && slices.Contains([]string{"added", "removed"}, payload.Action)
	if !supported {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored installation action"})
		return
	}
	digest := sha256.Sum256(body)
	_, err := s.mutateAuthorization(r.Context(), func(next *GitHubAuthorization) error {
		if old, ok := next.DeliveryDigests[delivery]; ok {
			if old != hex.EncodeToString(digest[:]) {
				return errAuthorizationDeliveryConflict
			}
			return errAuthorizationDuplicate
		}
		if next.DeliveryDigests == nil {
			next.DeliveryDigests = map[string]string{}
		}
		next.Deliveries = append(next.Deliveries, delivery)
		next.DeliveryDigests[delivery] = hex.EncodeToString(digest[:])
		if len(next.Deliveries) > 128 {
			delete(next.DeliveryDigests, next.Deliveries[0])
			next.Deliveries = next.Deliveries[1:]
		}
		next.Pending = true
		if slices.Contains([]string{"deleted", "suspend", "removed", "new_permissions_accepted"}, payload.Action) && !slices.Contains(next.Quarantined, payload.Installation.ID) {
			if len(next.Quarantined) >= 1000 {
				return fmt.Errorf("pending installation limit exceeded")
			}
			next.Quarantined = append(next.Quarantined, payload.Installation.ID)
		}
		return nil
	})
	if errors.Is(err, errAuthorizationDuplicate) {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "duplicate lifecycle event"})
		return
	}
	if errors.Is(err, errAuthorizationDeliveryConflict) {
		http.Error(w, "delivery ID already used with different content", http.StatusConflict)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	s.wakeGitHubAuthorization()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "authorization sync queued"})
}
