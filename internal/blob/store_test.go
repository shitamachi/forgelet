package blob

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCacheObjectBudgetDoesNotExpandArtifacts(t *testing.T) {
	limits := Limits{ObjectBytes: 4, CacheObjectBytes: 8, TotalBytes: 64, Objects: 10}
	store, err := Open(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind    string
		size    int
		limited bool
	}{{"artifact", 4, false}, {"artifact", 5, true}, {"cache", 8, false}, {"cache", 9, true}} {
		scope := Scope{Repository: "o/r", Kind: tc.kind, Namespace: "run", Version: strings.Repeat("a", 64)}
		_, err := store.Put(t.Context(), scope, tc.kind+string(rune('a'+tc.size)), "job", time.Hour, time.Now(), strings.NewReader(strings.Repeat("x", tc.size)))
		if errors.Is(err, ErrLimit) != tc.limited || err != nil && !tc.limited {
			t.Fatal(tc, err)
		}
	}
	for _, value := range []int64{-1, MaxCacheArchive + 1} {
		limits.CacheObjectBytes = value
		if limits.Validate() == nil {
			t.Fatal("accepted invalid cache limit", value)
		}
	}
	limits.CacheObjectBytes = MaxCacheArchive
	limits.TotalBytes = MaxCacheArchive - 1
	if limits.Validate() == nil {
		t.Fatal("cache exceeds total reservation budget")
	}
}

func TestTwoGiBCacheBudgetValidation(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		cache, artifact, total int64
		invalid                bool
	}{
		{"explicit 2GiB cache", 2 << 30, MaxArchive, 4 << 30, false},
		{"cache above maximum", (2 << 30) + 1, MaxArchive, 4 << 30, true},
		{"total below reservation", 2 << 30, MaxArchive, (2 << 30) - 1, true},
		{"artifact stays bounded", 2 << 30, MaxArchive + 1, 4 << 30, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := Limits{CacheObjectBytes: tc.cache, ObjectBytes: tc.artifact, TotalBytes: tc.total, Objects: 10}
			if err := limits.Validate(); (err != nil) != tc.invalid {
				t.Fatalf("validation error = %v, invalid = %v", err, tc.invalid)
			}
		})
	}
}

func TestStoreAtomicImmutableScopedRetention(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	limits := Limits{ObjectBytes: 1024, TotalBytes: 8192, Objects: 20}
	s, err := Open(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	scope := Scope{Repository: "o/r", Kind: "artifact", Namespace: "run1"}
	e, err := s.Put(ctx, scope, "a", "job", time.Hour, now, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, scope, "a", "job", time.Hour, now, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, scope, "a", "other", time.Hour, now, strings.NewReader("hello")); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	for _, other := range []Scope{{Repository: "x/r", Kind: "artifact", Namespace: "run1"}, {Repository: "o/r", Kind: "artifact", Namespace: "run2"}, {Repository: "o/r", Kind: "cache", Namespace: "run1"}} {
		if _, _, err := s.Get(other, "a", nil, now); !errors.Is(err, ErrNotFound) {
			t.Fatal("scope leak", err)
		}
	}
	if _, err := s.Put(ctx, scope, "large", "job", time.Hour, now, strings.NewReader(strings.Repeat("a", 1025))); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Put(cancelled, scope, "cancelled", "job", time.Hour, now, strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatal("unpublished uploads remain", len(files))
	}
	if err := os.Mkdir(filepath.Join(dir, ".upload-interrupted"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	got, file, err := s.Get(scope, "a", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(file)
	_ = file.Close()
	if got.ID != e.ID || string(data) != "hello" {
		t.Fatal(got, string(data))
	}
	n, err := s.Prune(ctx, now.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, _, err := s.Get(scope, "a", nil, now); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestCacheRaceVersionAndPrefix(t *testing.T) {
	s, err := Open(t.TempDir(), Limits{ObjectBytes: 100, TotalBytes: 5000, Objects: 100})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	scope := Scope{Repository: "o/r", Kind: "cache", Namespace: "refs/heads/main", Version: "v1"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Put(t.Context(), scope, "go-1", "job", time.Hour, now, strings.NewReader("cache")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(s.List(scope, now)) != 1 {
		t.Fatal("cache not atomic")
	}
	_, err = s.Put(t.Context(), scope, "go-2", "job", time.Hour, now.Add(time.Second), strings.NewReader("new"))
	if err != nil {
		t.Fatal(err)
	}
	e, f, err := s.Get(scope, "absent", []string{"no-", "go-"}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if e.Key != "go-2" {
		t.Fatal(e)
	}
	for _, change := range []string{"version", "ref", "repository"} {
		other := scope
		switch change {
		case "version":
			other.Version = "v2"
		case "ref":
			other.Namespace = "refs/pull/1/merge"
		case "repository":
			other.Repository = "x/y"
		}
		if _, _, err := s.Get(other, "go-1", []string{"go-"}, now); !errors.Is(err, ErrNotFound) {
			t.Fatal("cache isolation", err)
		}
	}
}

func TestArchiveRoundTripAndAttacks(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "app"), []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", ".hidden"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := Pack(t.Context(), workspace, []string{"dist"}, true, false, &archive); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := Unpack(t.Context(), dest, "download", bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "download", "app"))
	if err != nil || string(data) != "binary" {
		t.Fatal(err, string(data))
	}
	if _, err := os.Stat(filepath.Join(dest, "download", ".hidden")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("hidden file included")
	}
	for _, name := range []string{"../escape", "/absolute", "x/../../escape", "a\\b"} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			tw := tar.NewWriter(&b)
			_ = tw.WriteHeader(&tar.Header{Name: name, Size: 1, Mode: 0600})
			_, _ = tw.Write([]byte("x"))
			_ = tw.Close()
			if err := Unpack(t.Context(), dest, ".", &b); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dest, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Unpack(t.Context(), dest, "link", bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("escaped destination")
	}
	if err := os.Symlink("app", filepath.Join(workspace, "dist", "link")); err != nil {
		t.Fatal(err)
	}
	if err := Pack(t.Context(), workspace, []string{"dist"}, true, true, io.Discard); err == nil {
		t.Fatal("accepted archive link")
	}
	for _, value := range []string{"../a", "~/go", "**/dist", "a\n!b", "/tmp"} {
		if _, err := Paths(value); err == nil {
			t.Fatal("accepted paths", value)
		}
	}
}

func TestQuotaAllowsIdempotentRetryAndExpiredReplacement(t *testing.T) {
	limits := Limits{ObjectBytes: 5, TotalBytes: 5, Objects: 1}
	dir := t.TempDir()
	s, err := Open(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Repository: "o/r", Kind: "artifact", Namespace: "run"}
	now := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := s.Put(t.Context(), scope, "key", "job", time.Hour, now, strings.NewReader("hello")); err != nil {
			t.Fatal("retry at quota", err)
		}
	}
	if _, err := s.Put(t.Context(), scope, "other", "job", time.Hour, now, strings.NewReader("x")); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), scope, "key", "job", time.Hour, now.Add(time.Hour), strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	_, f, err := s.Get(scope, "key", nil, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "new" {
		t.Fatal(string(data), err)
	}
}

func TestSmallWritesUseActualQuotaAndFailuresReleaseReservations(t *testing.T) {
	s, err := Open(t.TempDir(), Limits{ObjectBytes: 8, CacheObjectBytes: 16, TotalBytes: 20, Objects: 10})
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Repository: "o/r", Kind: "cache", Namespace: "refs/heads/main", Version: "v1"}
	for _, tc := range []struct {
		key     string
		size    int
		limited bool
	}{{"large", 12, false}, {"small", 4, false}, {"overflow", 5, true}, {"fits remaining", 4, false}} {
		_, err := s.Put(t.Context(), scope, tc.key, "job", time.Hour, time.Now(), strings.NewReader(strings.Repeat("a", tc.size)))
		if errors.Is(err, ErrLimit) != tc.limited || err != nil && !tc.limited {
			t.Fatal(tc, err)
		}
		s.mu.Lock()
		reserved, uploads, used := s.reserved, s.uploads, s.used
		s.mu.Unlock()
		if reserved != 0 || uploads != 0 || used > 20 {
			t.Fatal(reserved, uploads, used)
		}
	}
	if len(s.List(scope, time.Now())) != 3 {
		t.Fatal("failed write persisted")
	}
	// A full store must still ACK an already committed immutable cache.
	if _, err := s.Put(t.Context(), scope, "large", "job", time.Hour, time.Now(), strings.NewReader("retry")); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentStreamsCannotOverbookActualBytes(t *testing.T) {
	s, err := Open(t.TempDir(), Limits{ObjectBytes: 32, TotalBytes: 40, Objects: 10})
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Repository: "o/r", Kind: "cache", Namespace: "main"}
	readers := []*io.PipeReader{}
	writers := []*io.PipeWriter{}
	results := make(chan error, 2)
	for i := range 2 {
		r, w := io.Pipe()
		readers = append(readers, r)
		writers = append(writers, w)
		t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
		go func() {
			_, err := s.Put(t.Context(), scope, string(rune('a'+i)), "job", time.Hour, time.Now(), r)
			_ = r.CloseWithError(err)
			results <- err
		}()
	}
	// Both uploads first hold sixteen real bytes, rather than 32-byte maxima.
	for _, w := range writers {
		if _, err := w.Write([]byte(strings.Repeat("x", 16))); err != nil {
			t.Fatal(err)
		}
	}
	// A pipe write unblocks at Read, so wait for both reservations to be recorded.
	for i := 0; i < 1000; i++ {
		s.mu.Lock()
		held := s.reserved
		s.mu.Unlock()
		if held == 32 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.mu.Lock()
	if s.reserved != 32 {
		s.mu.Unlock()
		t.Fatal("uploads did not reserve expected bytes")
	}
	s.mu.Unlock()
	for _, w := range writers {
		_ = w.Close()
	}
	for range readers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Put(t.Context(), scope, "too-large", "job", time.Hour, time.Now(), strings.NewReader(strings.Repeat("y", 9)))
	if !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if _, err = s.Put(t.Context(), scope, "fits", "job", time.Hour, time.Now(), strings.NewReader(strings.Repeat("z", 8))); err != nil {
		t.Fatal(err)
	}
}

func TestStreamingQuotaCancellationReleasesTemporaryBytes(t *testing.T) {
	s, err := Open(t.TempDir(), Limits{ObjectBytes: 32, TotalBytes: 32, Objects: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r, w := io.Pipe()
	defer func() { _ = r.Close(); _ = w.Close() }()
	scope := Scope{Repository: "o/r", Kind: "cache", Namespace: "main"}
	done := make(chan error, 1)
	go func() { _, err := s.Put(ctx, scope, "cancelled", "job", time.Hour, time.Now(), r); done <- err }()
	if _, err = w.Write([]byte(strings.Repeat("x", 16))); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = w.CloseWithError(context.Canceled)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.mu.Lock()
	used, reserved, uploads := s.used, s.reserved, s.uploads
	s.mu.Unlock()
	if used != 0 || reserved != 0 || uploads != 0 {
		t.Fatal(used, reserved, uploads)
	}
	if _, err = s.Put(t.Context(), scope, "after-cancel", "job", time.Hour, time.Now(), strings.NewReader(strings.Repeat("y", 32))); err != nil {
		t.Fatal(err)
	}
}
