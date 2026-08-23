package scheduler_test

import (
	"sort"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/scheduler"
)

func TestIDGenUniqueAndMonotonic(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	gen := scheduler.NewIDGen(func() time.Time { return base }, nil)

	ids := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		ids = append(ids, string(gen.NewRunID()))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			t.Fatalf("ids not strictly sortable at %d", i)
		}
	}

	// Job IDs share the generator; still unique against run IDs.
	jobID := gen.NewJobRunID()
	if seen[string(jobID)] {
		t.Fatalf("job id %s collides with a run id", jobID)
	}
}

func TestIDGenDefaultClock(t *testing.T) {
	gen := scheduler.NewIDGen(nil, nil)
	if gen.NewRunID() == "" || gen.NewJobRunID() == "" {
		t.Fatal("default clock must produce ids")
	}
}

func TestFixedIDGenClampsAndSorts(t *testing.T) {
	// Timestamp beyond the 48-bit ULID range is clamped, not panicked.
	gen := scheduler.NewFixedIDGen(1 << 60)
	var prev string
	for i := 0; i < 10; i++ {
		id := string(gen.NewRunID())
		if id <= prev {
			t.Fatalf("fixed ids must strictly increase: %s then %s", prev, id)
		}
		prev = id
	}
	if job := gen.NewJobRunID(); string(job) <= prev {
		t.Fatalf("job id %s must continue the sequence", job)
	}
}

// counterReader yields a strictly increasing byte on every read, providing
// deterministic but non-repeating entropy.
type counterReader struct{ next byte }

func (r *counterReader) Read(p []byte) (int, error) {
	for i := range p {
		r.next++
		p[i] = r.next
	}
	return len(p), nil
}

func TestIDGenInjectedEntropy(t *testing.T) {
	gen := scheduler.NewIDGen(func() time.Time { return time.Unix(42, 0).UTC() }, &counterReader{})
	first := gen.NewRunID()
	second := gen.NewRunID()
	if first == "" || second == "" || first == second {
		t.Fatalf("increasing entropy ids not distinct: %q %q", first, second)
	}
}
