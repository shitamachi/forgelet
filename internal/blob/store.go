// Package blob implements bounded local artifact/cache storage on the data PVC.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("blob not found")
var ErrConflict = errors.New("immutable blob conflict")
var ErrLimit = errors.New("blob quota exceeded")

const MaxArchive = 256 << 20
const MaxCacheArchive = 2 << 30

// CacheTransferTimeout bounds cross-worker transfers of large opaque archives.
const CacheTransferTimeout = 5 * time.Minute

type Limits struct {
	CacheObjectBytes int64 `yaml:"cache_object_bytes,omitempty"`
	ObjectBytes      int64 `yaml:"object_bytes"`
	TotalBytes       int64 `yaml:"total_bytes"`
	Objects          int   `yaml:"objects"`
}

func DefaultLimits() Limits {
	return Limits{ObjectBytes: MaxArchive, TotalBytes: 2 << 30, Objects: 4096}
}
func (l Limits) Validate() error {
	if l.ObjectBytes < 1 || l.ObjectBytes > MaxArchive || l.CacheObjectBytes < 0 || l.CacheObjectBytes > MaxCacheArchive || l.TotalBytes < max(l.ObjectBytes, l.CacheObjectBytes) || l.TotalBytes > 1<<40 || l.Objects < 1 || l.Objects > 100000 {
		return fmt.Errorf("invalid blob limits: artifact/object 1..256MiB, optional cache object 1..2GiB, total >= largest object and <=1TiB, objects 1..100000")
	}
	return nil
}

// ObjectLimit keeps larger opaque cache envelopes separate from artifact limits.
func (l Limits) ObjectLimit(kind string) int64 {
	if kind == "cache" && l.CacheObjectBytes > 0 {
		return l.CacheObjectBytes
	}
	return l.ObjectBytes
}
func (s *Store) ObjectLimit(kind string) int64 { return s.limits.ObjectLimit(kind) }

type Scope struct{ Repository, Kind, Namespace, Version string }
type Entry struct {
	Scope
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Owner     string    `json:"owner"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Store struct {
	dir            string
	limits         Limits
	mu             sync.Mutex
	entries        map[string]Entry
	used, reserved int64
	uploads        int
	readers        map[string]int
	accessed       map[string]time.Time
	touched        map[string]time.Time
}

// Open is called after the database's data-directory lock is held. Only fully
// renamed directories are visible; interrupted uploads are removed on restart.
func Open(directory string, limits Limits) (*Store, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create blob directory: %w", err)
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: directory, limits: limits, entries: map[string]Entry{}, readers: map[string]int{}, accessed: map[string]time.Time{}, touched: map[string]time.Time{}}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".upload-") {
			if err := os.RemoveAll(filepath.Join(directory, f.Name())); err != nil {
				return nil, err
			}
			continue
		}
		if len(f.Name()) != 64 || !f.IsDir() {
			return nil, fmt.Errorf("unexpected file in blob directory")
		}
		data, err := os.ReadFile(filepath.Join(directory, f.Name(), "metadata.json"))
		if err != nil {
			return nil, err
		}
		var e Entry
		if len(data) > 8192 || json.Unmarshal(data, &e) != nil || e.ID != f.Name() || e.ID != identity(e.Scope, e.Key) {
			return nil, fmt.Errorf("invalid blob metadata")
		}
		info, err := os.Lstat(filepath.Join(directory, e.ID, "data"))
		if err != nil || !info.Mode().IsRegular() || info.Size() != e.Size {
			return nil, fmt.Errorf("incomplete published blob %s", e.ID)
		}
		s.entries[e.ID] = e
		metadata, err := os.Stat(filepath.Join(directory, f.Name(), "metadata.json"))
		if err != nil {
			return nil, err
		}
		s.accessed[e.ID] = metadata.ModTime()
		s.touched[e.ID] = metadata.ModTime()
		s.used += e.Size
	}
	return s, nil
}

func identity(scope Scope, key string) string {
	h := sha256.New()
	for _, v := range []string{scope.Repository, scope.Kind, scope.Namespace, scope.Version, key} {
		_, _ = io.WriteString(h, v+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}
func valid(scope Scope, key string) bool {
	return scope.Repository != "" && scope.Namespace != "" && (scope.Kind == "artifact" || scope.Kind == "cache") && len(key) > 0 && len(key) <= 512 && !strings.ContainsAny(key, "\x00\r\n") && len(scope.Version) <= 128
}

func (s *Store) Put(ctx context.Context, scope Scope, key, owner string, ttl time.Duration, now time.Time, input io.Reader) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if !valid(scope, key) || owner == "" || ttl < time.Hour || ttl > 90*24*time.Hour {
		return Entry{}, fmt.Errorf("invalid blob metadata")
	}
	objectLimit := s.ObjectLimit(scope.Kind)
	s.mu.Lock()
	if old, ok := s.entries[identity(scope, key)]; ok && !now.Before(old.ExpiresAt) {
		if s.readers[old.ID] > 0 {
			s.mu.Unlock()
			return Entry{}, ErrLimit
		}
		tomb := filepath.Join(s.dir, ".upload-expired-"+old.ID)
		if err := os.Rename(filepath.Join(s.dir, old.ID), tomb); err != nil {
			s.mu.Unlock()
			return Entry{}, err
		}
		if err := os.RemoveAll(tomb); err != nil {
			s.mu.Unlock()
			return Entry{}, err
		}
		delete(s.entries, old.ID)
		delete(s.accessed, old.ID)
		delete(s.touched, old.ID)
		s.used -= old.Size
	}
	if old, ok := s.entries[identity(scope, key)]; ok && now.Before(old.ExpiresAt) {
		s.mu.Unlock()
		if scope.Kind == "cache" {
			return old, nil
		}
		if old.Owner != owner {
			return Entry{}, ErrConflict
		}
		// Verify a duplicate without reserving another full temporary object.
		// This permits acknowledgement recovery even at the storage quota.
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(&contextReader{ctx: ctx, r: input}, objectLimit+1))
		if err != nil {
			return Entry{}, err
		}
		if n > objectLimit {
			return Entry{}, ErrLimit
		}
		if n != old.Size || hex.EncodeToString(h.Sum(nil)) != old.Digest {
			return Entry{}, ErrConflict
		}
		return old, nil
	}
	// Count the upload immediately, but reserve bytes as they are written.
	// Reserving the per-object maximum made even tiny cache writes fail when
	// free quota was below 2 GiB, despite ample room for the actual object.
	if err := s.makeRoom(ctx, 0, 1, owner); err != nil {
		s.mu.Unlock()
		return Entry{}, err
	}
	s.uploads++
	var reserved int64
	active := true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if active {
			s.reserved -= reserved
			s.uploads--
		}
		s.mu.Unlock()
	}()

	dir, err := os.MkdirTemp(s.dir, ".upload-")
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	f, err := os.OpenFile(filepath.Join(dir, "data"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Entry{}, err
	}
	h := sha256.New()
	sink := quotaWriter{writer: io.MultiWriter(f, h), reserve: func(n int64) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		// An oversized object must not evict the entire cache before rejection.
		if n > objectLimit-reserved {
			return ErrLimit
		}
		if err := s.makeRoom(ctx, n, 0, owner); err != nil {
			return err
		}
		s.reserved += n
		reserved += n
		return nil
	}}
	n, copyErr := io.Copy(sink, io.LimitReader(&contextReader{ctx: ctx, r: input}, objectLimit+1))
	if copyErr == nil {
		copyErr = f.Sync()
	}
	err = errors.Join(copyErr, f.Close())
	if err != nil {
		return Entry{}, fmt.Errorf("upload blob: %w", err)
	}
	if n > objectLimit {
		return Entry{}, ErrLimit
	}
	e := Entry{Scope: scope, ID: identity(scope, key), Key: key, Owner: owner, Digest: hex.EncodeToString(h.Sum(nil)), Size: n, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	data, err := json.Marshal(e)
	if err != nil {
		return Entry{}, err
	}
	m, err := os.OpenFile(filepath.Join(dir, "metadata.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Entry{}, err
	}
	_, err = m.Write(data)
	if err == nil {
		err = m.Sync()
	}
	err = errors.Join(err, m.Close())
	if err != nil {
		return Entry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.entries[e.ID]; ok {
		if scope.Kind == "cache" || old.Owner == owner && old.Digest == e.Digest {
			return old, nil
		}
		return Entry{}, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if err := os.Chtimes(filepath.Join(dir, "metadata.json"), now, now); err != nil {
		return Entry{}, err
	}
	if err := syncDir(dir); err != nil {
		return Entry{}, err
	}
	if err := os.Rename(dir, filepath.Join(s.dir, e.ID)); err != nil {
		return Entry{}, err
	}
	s.entries[e.ID] = e
	s.accessed[e.ID] = now
	s.touched[e.ID] = now
	s.reserved -= reserved
	reserved = 0
	s.uploads--
	active = false
	s.used += n
	if err := syncDir(s.dir); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Get resolves exact keys first, then ordered prefix keys (newest match). Scope
// is always derived by the server from the authenticated job, never the client.
func (s *Store) Get(scope Scope, key string, prefixes []string, now time.Time) (Entry, *Reader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[identity(scope, key)]
	if !ok || !now.Before(e.ExpiresAt) {
		ok = false
		for _, prefix := range prefixes {
			for _, candidate := range s.entries {
				if candidate.Scope == scope && now.Before(candidate.ExpiresAt) && strings.HasPrefix(candidate.Key, prefix) && (!ok || candidate.CreatedAt.After(e.CreatedAt)) {
					e = candidate
					ok = true
				}
			}
			if ok {
				break
			}
		}
	}
	if !ok {
		return Entry{}, nil, ErrNotFound
	}
	f, err := os.Open(filepath.Join(s.dir, e.ID, "data"))
	if err != nil {
		return Entry{}, nil, err
	}
	// Persist coarse access time in metadata mtime, at most once per minute.
	// Reads pin the inode until Close so eviction cannot free only logical space.
	if now.Sub(s.touched[e.ID]) >= time.Minute {
		if err := os.Chtimes(filepath.Join(s.dir, e.ID, "metadata.json"), now, now); err != nil {
			_ = f.Close()
			return Entry{}, nil, err
		}
	}
	if now.Sub(s.touched[e.ID]) >= time.Minute {
		s.touched[e.ID] = now
	}
	s.accessed[e.ID] = now
	s.readers[e.ID]++
	return e, &Reader{File: f, store: s, id: e.ID}, nil
}

func (s *Store) List(scope Scope, now time.Time) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []Entry{}
	for _, e := range s.entries {
		if e.Scope == scope && now.Before(e.ExpiresAt) {
			result = append(result, e)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}
func (s *Store) Prune(ctx context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, e := range s.entries {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		if now.Before(e.ExpiresAt) || s.readers[id] > 0 {
			continue
		}
		// Rename out of the published namespace before removal. A crash leaves
		// only an upload-shaped directory, which Open cleans up.
		tomb := filepath.Join(s.dir, ".upload-gc-"+id)
		if err := os.Rename(filepath.Join(s.dir, id), tomb); err != nil {
			return n, err
		}
		if err := os.RemoveAll(tomb); err != nil {
			return n, err
		}
		delete(s.entries, id)
		delete(s.accessed, id)
		delete(s.touched, id)
		s.used -= e.Size
		n++
	}
	return n, syncDir(s.dir)
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// quotaWriter reserves each buffer before touching the filesystem, so stored
// data plus all in-flight temporary files never exceed the shared byte budget.
type quotaWriter struct {
	writer  io.Writer
	reserve func(int64) error
}

func (w quotaWriter) Write(data []byte) (int, error) {
	if err := w.reserve(int64(len(data))); err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}

// Reader pins a published blob. Closing releases its physical-byte reservation.
type Reader struct {
	*os.File
	store *Store
	id    string
	once  sync.Once
	err   error
}

func (r *Reader) Close() error {
	r.once.Do(func() {
		r.err = r.File.Close()
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
		r.store.readers[r.id]--
		if r.store.readers[r.id] == 0 {
			delete(r.store.readers, r.id)
		}
	})
	return r.err
}

// makeRoom is called with mu held. Artifact retention is never shortened.
// In-flight bytes, active readers and this producer's own published objects are
// protected; if they exhaust capacity the caller receives an honest quota error.
func (s *Store) makeRoom(ctx context.Context, bytes int64, objects int, owner string) error {
	fits := func() bool {
		return bytes <= s.limits.TotalBytes-s.used-s.reserved && len(s.entries)+s.uploads+objects <= s.limits.Objects
	}
	if fits() {
		return nil
	}
	candidates := []Entry{}
	for _, e := range s.entries {
		if e.Kind == "cache" && e.Owner != owner && s.readers[e.ID] == 0 {
			candidates = append(candidates, e)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := s.accessed[candidates[i].ID], s.accessed[candidates[j].ID]
		if a.Equal(b) {
			return candidates[i].ID < candidates[j].ID
		}
		return a.Before(b)
	})
	for _, e := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		tomb := filepath.Join(s.dir, ".upload-evicted-"+e.ID)
		if err := os.Rename(filepath.Join(s.dir, e.ID), tomb); err != nil {
			return err
		}
		// Don't release the accounting until physical removal succeeds.
		if err := os.RemoveAll(tomb); err != nil {
			return err
		}
		delete(s.entries, e.ID)
		delete(s.accessed, e.ID)
		delete(s.touched, e.ID)
		s.used -= e.Size
		if err := syncDir(s.dir); err != nil {
			return err
		}
		if fits() {
			return nil
		}
	}
	return ErrLimit
}
