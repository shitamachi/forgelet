package scheduler

import (
	"crypto/rand"
	"io"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// Clock is the injectable time source for all scheduler use cases.
type Clock func() time.Time

// SystemClock is the default Clock.
func SystemClock() time.Time { return time.Now() }

// IDGen generates sortable unique IDs. Entropy and clock are injected so
// tests can be deterministic.
type IDGen struct {
	mu  sync.Mutex
	mon *ulid.MonotonicEntropy
	now Clock
}

// NewIDGen returns an IDGen using the given clock and entropy. A nil entropy
// falls back to a process-local monotonic reader over crypto/rand.
func NewIDGen(now Clock, entropy io.Reader) *IDGen {
	if now == nil {
		now = SystemClock
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	return &IDGen{
		mon: ulid.Monotonic(entropy, 0),
		now: now,
	}
}

// NewRunID mints a fresh WorkflowRun ID.
func (g *IDGen) NewRunID() model.RunID {
	return model.RunID(g.new().String())
}

// NewJobRunID mints a fresh JobRun ID.
func (g *IDGen) NewJobRunID() model.JobRunID {
	return model.JobRunID(g.new().String())
}

func (g *IDGen) new() ulid.ULID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(g.now()), g.mon)
}

// maxULIDTimestamp is the largest millisecond timestamp a ULID can carry
// (48 bits).
const maxULIDTimestamp = uint64(1)<<48 - 1

// FixedIDGen is a deterministic IDGen for tests: IDs increment by one from a
// base ULID, so ordering matches creation order.
type FixedIDGen struct {
	mu    sync.Mutex
	base  uint64
	count uint64
}

// NewFixedIDGen returns an IDGen producing ULIDs with a fixed timestamp (ms)
// and strictly increasing entropy bits.
func NewFixedIDGen(ms uint64) *FixedIDGen {
	if ms > maxULIDTimestamp {
		ms = maxULIDTimestamp // clamp: test helper, malformed input must not panic
	}
	return &FixedIDGen{base: ms}
}

// NewRunID mints a fresh WorkflowRun ID.
func (g *FixedIDGen) NewRunID() model.RunID {
	return model.RunID(g.new().String())
}

// NewJobRunID mints a fresh JobRun ID.
func (g *FixedIDGen) NewJobRunID() model.JobRunID {
	return model.JobRunID(g.new().String())
}

func (g *FixedIDGen) new() ulid.ULID {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.count++
	return ulid.MustNew(g.base, ulidEntropy(g.count))
}

func ulidEntropy(n uint64) io.Reader {
	var b [10]byte
	for i := range b {
		b[i] = byte(n >> (8 * (len(b) - 1 - i)))
	}
	return newByteReader(b[:])
}

type byteReader struct {
	b []byte
	i int
}

func newByteReader(b []byte) *byteReader { return &byteReader{b: b} }
func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
