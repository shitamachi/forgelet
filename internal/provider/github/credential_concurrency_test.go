package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlowTokenDoesNotBlockOtherRepositoryOrCancelledWaiter(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var slowCalls atomic.Int32
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Repositories []string `json:"repositories"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		if len(b.Repositories) == 1 && b.Repositories[0] == "slow" {
			if slowCalls.Add(1) == 1 {
				close(entered)
			}
			<-release
		}
		writeToken(t, w)
	})
	if _, err := c.RepositoryToken(t.Context(), 7, "o/cached"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.RepositoryToken(t.Context(), 7, "o/slow"); done <- err }()
	<-entered
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	for _, repo := range []string{"o/cached", "o/new"} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		_, err := c.RepositoryToken(ctx, 7, repo)
		cancel()
		if err != nil {
			t.Fatalf("unrelated %s blocked: %v", repo, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.RepositoryToken(ctx, 7, "o/slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if slowCalls.Load() != 1 {
		t.Fatal("duplicate pending token mint")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCredentialErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		err   error
		retry bool
	}{{&apiError{status: 401}, false}, {&apiError{status: 403}, false}, {&apiError{status: 404}, false}, {&apiError{status: 422}, false}, {&apiError{status: 429}, true}, {&apiError{status: 503}, true}, {&apiError{status: 403, rateLimited: true}, true}, {context.DeadlineExceeded, true}, {errors.New("private upstream detail"), true}} {
		reason, retry := CredentialFailure(tc.err)
		if retry != tc.retry || reason == "" || reason == tc.err.Error() {
			t.Fatal(reason, retry)
		}
	}
}
