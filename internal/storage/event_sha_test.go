package storage

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestPinnedPRMergeSurvivesRestartAndDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	event := run.Event{Name: "pull_request", DeliveryID: "pending-pr", Repository: "owner/repo", HeadSHA: strings.Repeat("b", 40), BeforeSHA: strings.Repeat("c", 40), Payload: json.RawMessage(`{"pull_request":{"merge_commit_sha":null,"title":"keep original"}}`)}
	if _, err = s.PutEvent(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	pin, err := s.PinEventSHA(t.Context(), event.DeliveryID, sha)
	if err != nil || pin.SHA != sha {
		t.Fatal(pin, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if created, err := s.PutEvent(t.Context(), event); err != nil || created {
		t.Fatal(created, err)
	}
	again, err := s.PinEventSHA(t.Context(), event.DeliveryID, strings.Repeat("d", 40))
	if err != nil || again.SHA != sha || again.HeadSHA != event.HeadSHA || again.BeforeSHA != event.BeforeSHA || !strings.Contains(string(again.Payload), `"title":"keep original"`) || !strings.Contains(string(again.Payload), sha) {
		t.Fatal(again, err)
	}
	if _, err = s.PinEventSHA(t.Context(), event.DeliveryID, "not-sha"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = s.FinishEvent(t.Context(), event.DeliveryID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PinEventSHA(t.Context(), event.DeliveryID, sha); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
