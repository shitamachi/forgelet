package server

import (
	"context"
	"errors"
	"testing"

	gh "github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/run"
)

type diffProvider struct {
	*fakeProvider
	failure error
}

func (p diffProvider) ChangedFiles(context.Context, run.Event) ([]string, error) {
	return nil, p.failure
}

func TestPathFilterDiagnosticVersusRetry(t *testing.T) {
	for _, permanent := range []bool{true, false} {
		t.Run(map[bool]string{true: "permanent", false: "retry"}[permanent], func(t *testing.T) {
			h := newHarness(t, 2)
			h.provider.files = map[string][]byte{".github/workflows/ci.yml": []byte("on: {push: {paths: ['src/**']}}\njobs:\n  test:\n    runs-on: small\n    steps: [{run: echo ok}]\n")}
			failure := errors.New("temporary outage")
			if permanent {
				failure = gh.ErrComparisonUnsupported
			}
			h.server.provider = diffProvider{h.provider, failure}
			if status, body := h.webhook("diff", nil); status != 202 {
				t.Fatal(status, string(body))
			}
			err := h.server.Tick(t.Context())
			if permanent && err != nil || !permanent && err == nil {
				t.Fatal(err)
			}
			pending, err := h.store.PendingEvents(t.Context())
			if err != nil || permanent && len(pending) != 0 || !permanent && len(pending) != 1 {
				t.Fatal(pending, err)
			}
			events, err := h.store.Events(t.Context())
			if err != nil || len(events) != 1 || permanent && events[0].Error == "" {
				t.Fatal(events, err)
			}
			runs, err := h.store.ListRuns(t.Context())
			if err != nil || len(runs) != 0 {
				t.Fatal(runs, err)
			}
		})
	}
}
