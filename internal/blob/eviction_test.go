package blob

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestCacheEvictionLRURestartAndProtectedReaders(t *testing.T) {
	dir := t.TempDir()
	limits := Limits{ObjectBytes: 8, TotalBytes: 24, Objects: 3}
	s, err := Open(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	cache := Scope{Repository: "o/r", Kind: "cache", Namespace: "main"}
	artifact := Scope{Repository: "o/r", Kind: "artifact", Namespace: "run"}
	put := func(scope Scope, key, owner string, at time.Time) {
		t.Helper()
		if _, err := s.Put(t.Context(), scope, key, owner, time.Hour, at, strings.NewReader("12345678")); err != nil {
			t.Fatal(err)
		}
	}
	put(cache, "old", "a", now)
	put(cache, "warm", "b", now.Add(time.Minute))
	put(artifact, "report", "c", now)
	_, f, err := s.Get(cache, "old", nil, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	s, err = Open(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	put(cache, "new", "d", now.Add(4*time.Minute))
	if _, _, err := s.Get(cache, "warm", nil, now.Add(4*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatal("LRU did not survive restart", err)
	}
	_, pinned, err := s.Get(cache, "old", nil, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	put(cache, "next", "e", now.Add(5*time.Minute))
	if data, err := io.ReadAll(pinned); err != nil || string(data) != "12345678" {
		t.Fatal("active read evicted", err)
	}
	_ = pinned.Close()
	_ = pinned.Close()
	_, f, err = s.Get(artifact, "report", nil, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal("artifact evicted", err)
	}
	_ = f.Close()
	if s.used+s.reserved > limits.TotalBytes {
		t.Fatal("quota exceeded")
	}
}

func TestPressureNeverEvictsArtifactsReadersOrCurrentProducer(t *testing.T) {
	for _, kind := range []string{"artifact", "reader", "producer"} {
		t.Run(kind, func(t *testing.T) {
			s, err := Open(t.TempDir(), Limits{ObjectBytes: 8, TotalBytes: 8, Objects: 1})
			if err != nil {
				t.Fatal(err)
			}
			scope := Scope{Repository: "o/r", Kind: "cache", Namespace: "main"}
			if kind == "artifact" {
				scope.Kind = "artifact"
			}
			if _, err := s.Put(t.Context(), scope, "first", "producer", time.Hour, time.Now(), strings.NewReader("12345678")); err != nil {
				t.Fatal(err)
			}
			if kind == "reader" {
				_, f, err := s.Get(scope, "first", nil, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = f.Close() }()
			}
			owner := "other"
			if kind == "producer" {
				owner = "producer"
			}
			_, err = s.Put(t.Context(), Scope{Repository: "o/r", Kind: "cache", Namespace: "main"}, "second", owner, time.Hour, time.Now(), strings.NewReader("x"))
			if !errors.Is(err, ErrLimit) {
				t.Fatal(err)
			}
		})
	}
}
