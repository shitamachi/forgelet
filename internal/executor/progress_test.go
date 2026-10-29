package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestProgressPendingACKSurvivesCancellationAndFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var received []run.ProgressEvent
	var mu sync.Mutex
	options := remoteTestOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var e run.ProgressEvent
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		received = append(received, e)
		count := len(received)
		mu.Unlock()
		if count == 1 {
			// Simulate acceptance followed by a lost acknowledgement.
			cancel()
			http.Error(w, "response lost", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int64{"seq": e.Seq})
	})
	client, err := newRemoteClient(options)
	if err != nil {
		t.Fatal(err)
	}
	reporter := progressReporter{client: client}
	if err := reporter.emit(ctx, run.ProgressEvent{Kind: "phase", Phase: "executing"}); err == nil {
		t.Fatal("lost ACK was treated as received")
	}
	if reporter.pending == nil || reporter.seq != 0 {
		t.Fatal("pending event was dropped")
	}
	if err := reporter.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if reporter.seq != 1 || len(received) != 2 || !reflect.DeepEqual(received[0], received[1]) {
		t.Fatal("flush changed event identity", received)
	}
}

func TestNativeAnnotationProtocolEscapesMasksAndStopCommands(t *testing.T) {
	var events []run.ProgressEvent
	plan := run.Plan{Steps: []run.Step{{ID: "run", Run: `printf '::add-mask::secret-value\n'
printf '::stop-commands::resume\n'
printf '::warning::ignored while stopped\n'
printf '::resume::\n'
printf '::notice title=hello::secret-value%%0Anext%%25line\n'
printf 'ordinary text warning: do not interpret\n'`}}}
	result, err := Execute(t.Context(), plan, Options{Workspace: t.TempDir(), Progress: func(_ context.Context, e run.ProgressEvent) error { events = append(events, e); return nil }})
	if err != nil || result.State != run.Succeeded {
		t.Fatal(result, err)
	}
	var annotations []run.ProgressEvent
	for _, e := range events {
		if e.Kind == "annotation" {
			annotations = append(annotations, e)
		}
	}
	if len(annotations) != 1 || annotations[0].Message != "***\nnext%line" || annotations[0].Level != "notice" {
		t.Fatal(annotations)
	}
	if events[0].Phase != "executing" || events[len(events)-1].Phase != "cleanup" {
		t.Fatal(events)
	}
}
