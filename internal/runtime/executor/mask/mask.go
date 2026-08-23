// Package mask replaces registered secret values with *** before anything
// is written to logs (spec 0001 FR-5.5: masking happens before output).
package mask

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// Masker holds the registered secret values. Empty values are ignored —
// masking "" would corrupt every log line.
type Masker struct {
	mu   sync.Mutex
	vals []string
}

// New returns an empty Masker.
func New() *Masker { return &Masker{} }

// Add registers a secret value.
func (m *Masker) Add(v string) {
	if v == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.vals {
		if existing == v {
			return
		}
	}
	m.vals = append(m.vals, v)
}

// Apply replaces every registered value in s with ***. Longer values are
// replaced first so overlapping secrets do not leak fragments.
func (m *Masker) Apply(s string) string {
	m.mu.Lock()
	vals := append([]string(nil), m.vals...)
	m.mu.Unlock()
	if len(vals) == 0 {
		return s
	}
	sort.SliceStable(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		if v != "" {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// Handler wraps a slog.Handler, masking the message and string attribute
// values before they reach the inner handler. Registered values added after
// handler construction take effect immediately.
type Handler struct {
	inner slog.Handler
	m     *Masker
}

// NewHandler wraps inner.
func NewHandler(inner slog.Handler, m *Masker) *Handler {
	return &Handler{inner: inner, m: m}
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	masked := slog.NewRecord(r.Time, r.Level, h.m.Apply(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindString {
			a.Value = slog.StringValue(h.m.Apply(a.Value.String()))
		}
		masked.AddAttrs(a)
		return true
	})
	return h.inner.Handle(ctx, masked)
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return NewHandler(h.inner.WithAttrs(attrs), h.m)
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	return NewHandler(h.inner.WithGroup(name), h.m)
}
