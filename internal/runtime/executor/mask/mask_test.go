package mask

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestMaskerLongestFirst(t *testing.T) {
	m := New()
	m.Add("super-secret-token")
	m.Add("super-secret") // prefix of the longer value
	got := m.Apply("leak: super-secret-token and super-secret")
	if strings.Contains(got, "super-secret") {
		t.Errorf("fragment leaked: %q", got)
	}
	if got != "leak: *** and ***" {
		t.Errorf("got %q", got)
	}
}

func TestMaskerIgnoresEmptyAndDuplicates(t *testing.T) {
	onlyEmpty := New()
	onlyEmpty.Add("")
	if got := onlyEmpty.Apply("plain text"); got != "plain text" {
		t.Errorf("empty secret corrupted output: %q", got)
	}

	dup := New()
	dup.Add("needle")
	dup.Add("needle")
	got := dup.Apply("a needle and needle")
	if got != "a *** and ***" {
		t.Errorf("duplicate registration double-masked: %q", got)
	}
}

func TestMaskerNoSecrets(t *testing.T) {
	m := New()
	if got := m.Apply("untouched"); got != "untouched" {
		t.Errorf("got %q", got)
	}
}

func TestHandlerMasksMessagesAndAttrs(t *testing.T) {
	m := New()
	m.Add("hunter2")

	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(NewHandler(inner, m))

	logger.Info("password=hunter2", "detail", "token hunter2 leaked", "count", 3)
	out := buf.String()
	if strings.Contains(out, "hunter2") {
		t.Errorf("secret leaked into logs: %s", out)
	}
	if !strings.Contains(out, "***") || !strings.Contains(out, "\"count\":3") {
		t.Errorf("masking lost content: %s", out)
	}
}

func TestHandlerLateAddTakesEffect(t *testing.T) {
	m := New()
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	logger := slog.New(NewHandler(inner, m))

	logger.Info("before secret")
	m.Add("late-secret")
	logger.Info("now with late-secret")
	out := buf.String()
	if strings.Contains(out, "late-secret") {
		t.Errorf("late-registered secret leaked: %s", out)
	}
	if !strings.Contains(out, "before secret") {
		t.Error("earlier log line lost")
	}
}

func TestHandlerEnabledPassthrough(t *testing.T) {
	m := New()
	h := NewHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil), m)
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("error level must be enabled")
	}
	if h.WithAttrs(nil) == nil || h.WithGroup("g") == nil {
		t.Error("With* must return handlers")
	}
}
