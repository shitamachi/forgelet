package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/scheduler"
	"github.com/shitamachi/forgelet/internal/storage/memory"
)

const pushBody = `{
  "ref": "refs/heads/main",
  "after": "abc123def456abc123def456abc123def456abc1",
  "repository": {"name": "forgelet", "owner": {"login": "shitamachi"}},
  "pusher": {"name": "guo"}
}`

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	secret, body := "whsec", pushBody
	good := sign(secret, body)

	cases := []struct {
		name   string
		secret string
		body   string
		header string
		wantOK bool
	}{
		{"valid", secret, body, good, true},
		{"tampered body", secret, body + " ", good, false},
		{"wrong secret", "other", body, good, false},
		{"missing prefix", secret, body, strings.TrimPrefix(good, "sha256="), false},
		{"not hex", secret, body, "sha256=zzzz", false},
		{"empty", secret, body, "", false},
	}
	for _, tc := range cases {
		err := VerifySignature([]byte(tc.secret), []byte(tc.body), tc.header)
		if tc.wantOK && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.wantOK && !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: want ErrBadSignature, got %v", tc.name, err)
		}
	}
}

func TestDecodePush(t *testing.T) {
	ev, err := DecodePush([]byte(pushBody), "d-1")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := model.Event{
		Provider: "github", Name: "push", DeliveryID: "d-1",
		Repository: model.RepositoryRef{Provider: "github", Owner: "shitamachi", Name: "forgelet"},
		Ref:        "refs/heads/main",
		SHA:        "abc123def456abc123def456abc123def456abc1",
		Actor:      "guo",
	}
	if ev != want {
		t.Errorf("decoded %+v, want %+v", ev, want)
	}

	deleted := strings.Replace(pushBody, `"after"`, `"deleted": true, "after"`, 1)
	if _, err := DecodePush([]byte(deleted), "d-2"); !errors.Is(err, ErrIgnoredPush) {
		t.Errorf("deleted push: want ErrIgnoredPush, got %v", err)
	}
	zeroSHA := strings.Replace(pushBody, "abc123def456abc123def456abc123def456abc1", "0000000000000000000000000000000000000000", 1)
	if _, err := DecodePush([]byte(zeroSHA), "d-3"); !errors.Is(err, ErrIgnoredPush) {
		t.Errorf("zero after: want ErrIgnoredPush, got %v", err)
	}
	noRepo := strings.Replace(pushBody, `"forgelet"`, `""`, 1)
	if _, err := DecodePush([]byte(noRepo), "d-4"); !errors.Is(err, ErrMalformedPayload) {
		t.Errorf("missing repo: want ErrMalformedPayload, got %v", err)
	}
	if _, err := DecodePush([]byte("not json"), "d-5"); !errors.Is(err, ErrMalformedPayload) {
		t.Errorf("bad json: want ErrMalformedPayload, got %v", err)
	}
}

func newHandler(t *testing.T) (*WebhookHandler, *memory.DurableStore) {
	t.Helper()
	store := memory.NewDurableStore(func() time.Time { return time.Unix(0, 0).UTC() }, nil)
	compiler := staticCompiler{jobs: []model.JobIntent{{JobKey: "test", RunnerClass: "k3s-small"}}}
	ing := scheduler.NewIngestor(store, compiler, scheduler.NewFixedIDGen(42), nil)
	return NewWebhookHandler([]byte("whsec"), ing), store
}

type staticCompiler struct {
	jobs []model.JobIntent
}

func (c staticCompiler) Compile(context.Context, model.Event, []byte) ([]model.JobIntent, error) {
	return c.jobs, nil
}

func post(h http.Handler, event, delivery, signature string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-Hub-Signature-256", signature)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWebhookHandlerEndToEnd(t *testing.T) {
	h, store := newHandler(t)

	// AC 1: bad signature never touches durable storage.
	rec := post(h, "push", "d-bad", "sha256=0000", pushBody)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad signature: status %d, want 403", rec.Code)
	}
	if len(store.Runs()) != 0 {
		t.Fatal("durable write happened despite bad signature")
	}

	// AC 2: same delivery twice -> one run, payload preserved byte-for-byte.
	rec = post(h, "push", "d-1", sign("whsec", pushBody), pushBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("first delivery: %d %s", rec.Code, rec.Body.String())
	}
	rec = post(h, "push", "d-1", sign("whsec", pushBody), pushBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"created":false`) {
		t.Errorf("replay must report created=false: %s", rec.Body.String())
	}
	if runs := store.Runs(); len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	rec2, _, err := store.RecordDelivery(context.Background(), model.Delivery{
		Key: model.DeliveryKey{Provider: "github", DeliveryID: "d-1"},
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if string(rec2.Payload) != pushBody {
		t.Error("ProviderPayload not preserved byte-for-byte")
	}

	// AC 3: branch deletion and unknown events are 2xx, no new run.
	del := strings.Replace(pushBody, `"after"`, `"deleted": true, "after"`, 1)
	if rec := post(h, "push", "d-del", sign("whsec", del), del); rec.Code != http.StatusOK {
		t.Fatalf("deleted push: %d", rec.Code)
	}
	if rec := post(h, "ping", "d-ping", sign("whsec", `{}`), `{}`); rec.Code != http.StatusOK {
		t.Fatalf("ping: %d", rec.Code)
	}
	if runs := store.Runs(); len(runs) != 1 {
		t.Fatalf("runs after ignored events = %d, want 1", len(runs))
	}

	// Missing delivery id.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/github", strings.NewReader(pushBody))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", sign("whsec", pushBody))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing delivery id: %d", rec.Code)
	}

	// Malformed push payload with valid signature.
	if rec := post(h, "push", "d-mal", sign("whsec", `{}`), `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed push: %d", rec.Code)
	}
}
