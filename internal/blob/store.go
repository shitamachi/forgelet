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

type Limits struct {
	ObjectBytes int64 `yaml:"object_bytes"`
	TotalBytes  int64 `yaml:"total_bytes"`
	Objects     int   `yaml:"objects"`
}

func DefaultLimits() Limits {
	return Limits{ObjectBytes: MaxArchive, TotalBytes: 2 << 30, Objects: 4096}
}
func (l Limits) Validate() error {
	if l.ObjectBytes < 1 || l.ObjectBytes > MaxArchive || l.TotalBytes < l.ObjectBytes || l.TotalBytes > 1<<40 || l.Objects < 1 || l.Objects > 100000 {
		return fmt.Errorf("invalid blob limits: object 1..256MiB, total >= object and <=1TiB, objects 1..100000")
	}
	return nil
}

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
	s := &Store{dir: directory, limits: limits, entries: map[string]Entry{}}
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
	s.mu.Lock()
	if old, ok := s.entries[identity(scope, key)]; ok && !now.Before(old.ExpiresAt) {
		tomb := filepath.Join(s.dir, ".upload-expired-"+old.ID)
		if err := os.Rename(filepath.Join(s.dir, old.ID), tomb); err != nil {
			s.mu.Unlock()
			return Entry{}, err
		}
		delete(s.entries, old.ID)
		s.used -= old.Size
		if err := os.RemoveAll(tomb); err != nil {
			s.mu.Unlock()
			return Entry{}, err
		}
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
		n, err := io.Copy(h, io.LimitReader(&contextReader{ctx: ctx, r: input}, s.limits.ObjectBytes+1))
		if err != nil {
			return Entry{}, err
		}
		if n > s.limits.ObjectBytes {
			return Entry{}, ErrLimit
		}
		if n != old.Size || hex.EncodeToString(h.Sum(nil)) != old.Digest {
			return Entry{}, ErrConflict
		}
		return old, nil
	}
	// Reserve the maximum object size before accepting bytes, including every
	// concurrent temporary upload in the global bound.
	if s.used+s.reserved+s.limits.ObjectBytes > s.limits.TotalBytes || len(s.entries)+s.uploads >= s.limits.Objects {
		s.mu.Unlock()
		return Entry{}, ErrLimit
	}
	s.reserved += s.limits.ObjectBytes
	s.uploads++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.reserved -= s.limits.ObjectBytes; s.uploads--; s.mu.Unlock() }()
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
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(&contextReader{ctx: ctx, r: input}, s.limits.ObjectBytes+1))
	if copyErr == nil {
		copyErr = f.Sync()
	}
	err = errors.Join(copyErr, f.Close())
	if err != nil {
		return Entry{}, fmt.Errorf("upload blob: %w", err)
	}
	if n > s.limits.ObjectBytes {
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
	if err := syncDir(dir); err != nil {
		return Entry{}, err
	}
	if err := os.Rename(dir, filepath.Join(s.dir, e.ID)); err != nil {
		return Entry{}, err
	}
	s.entries[e.ID] = e
	s.used += n
	if err := syncDir(s.dir); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Get resolves exact keys first, then ordered prefix keys (newest match). Scope
// is always derived by the server from the authenticated job, never the client.
func (s *Store) Get(scope Scope, key string, prefixes []string, now time.Time) (Entry, *os.File, error) {
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
	return e, f, nil
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
		if now.Before(e.ExpiresAt) {
			continue
		}
		// Rename out of the published namespace before removal. A crash leaves
		// only an upload-shaped directory, which Open cleans up.
		tomb := filepath.Join(s.dir, ".upload-gc-"+id)
		if err := os.Rename(filepath.Join(s.dir, id), tomb); err != nil {
			return n, err
		}
		delete(s.entries, id)
		s.used -= e.Size
		n++
		if err := os.RemoveAll(tomb); err != nil {
			return n, err
		}
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
