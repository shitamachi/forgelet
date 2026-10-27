package executor

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shitamachi/forgelet/internal/blob"
)

// The job-local v1/v2 cache facade transports opaque SDK archives inside a bounded
// tar object. Storage scope, immutability, retention and quotas remain server-owned.
type actCache struct {
	objectBytes                  int64
	exactDownloads               map[string]int
	restoreMu                    sync.Mutex
	downloadBytes                int64
	ctx                          context.Context
	services                     Services
	dir, token, capability, base string
	server                       *http.Server
	listener                     net.Listener
	mu                           sync.Mutex
	next                         int
	uploads                      map[int]*cacheUpload
	downloads                    map[int]string
}
type cacheUpload struct {
	Key, Version, File string
	Ranges             map[int64]int64
	Committed          bool
	Size               int64
	BlockDigests       map[string][32]byte
	ListDigest         [32]byte
	Blocks             map[string]string
	Sealed             bool
	Staged             int64
}

func startActCache(ctx context.Context, services Services, workspace string) (*actCache, error) {
	return startActCacheAt(ctx, services, "127.0.0.1", 0)
}
func startActCacheAt(ctx context.Context, services Services, host string, objectBytes int64) (*actCache, error) {
	if objectBytes == 0 {
		objectBytes = blob.MaxArchive
	}
	if objectBytes < 2048 || objectBytes > blob.MaxCacheArchive {
		return nil, fmt.Errorf("invalid act cache object budget")
	}
	if net.ParseIP(host) == nil {
		return nil, fmt.Errorf("invalid act cache listen IP")
	}
	dir, err := os.MkdirTemp("", "forgelet-cache-")
	if err != nil {
		return nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	c := &actCache{objectBytes: objectBytes, ctx: ctx, services: services, dir: dir, capability: hex.EncodeToString(random[:]), listener: listener, uploads: map[int]*cacheUpload{}, downloads: map[int]string{}, exactDownloads: map[string]int{}}
	c.token = cacheRuntimeToken(random[:], time.Now())
	c.base = "http://" + listener.Addr().String() + "/" + c.capability + "/"
	c.server = &http.Server{Handler: c, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 5 * time.Minute, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = c.server.Serve(listener) }()
	return c, nil
}
func (c *actCache) URL() string { return c.base }
func (c *actCache) Close()      { _ = c.server.Close(); _ = os.RemoveAll(c.dir) }
func (c *actCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/"+cacheServicePath) && c.services != nil && c.ctx.Err() == nil {
		if r.Header.Get("Authorization") != "Bearer "+c.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c.cacheV2(w, r, strings.TrimPrefix(r.URL.Path, "/"+cacheServicePath))
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/"+c.capability+"/") || c.services == nil || c.ctx.Err() != nil {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/"+c.capability+"/")
	if strings.HasPrefix(path, "archive/") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		id, _ := strconv.Atoi(strings.TrimPrefix(path, "archive/"))
		c.mu.Lock()
		name := c.downloads[id]
		c.mu.Unlock()
		if name == "" {
			http.NotFound(w, r)
			return
		}
		serveCacheFile(w, r, name)
		return
	}
	if strings.HasPrefix(path, "blob/") {
		c.azureUpload(w, r, strings.TrimPrefix(path, "blob/"))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+c.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if strings.HasPrefix(path, cacheServicePath) {
		c.cacheV2(w, r, strings.TrimPrefix(path, cacheServicePath))
		return
	}
	path = strings.TrimPrefix(path, "_apis/artifactcache/")
	switch {
	case path == "cache" && r.Method == http.MethodGet:
		c.restore(w, r)
	case path == "caches" && r.Method == http.MethodPost:
		c.reserve(w, r)
	case strings.HasPrefix(path, "caches/"):
		c.upload(w, r, strings.TrimPrefix(path, "caches/"))
	default:
		http.NotFound(w, r)
	}
}
func cacheJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func cacheVersion(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func (c *actCache) restore(w http.ResponseWriter, r *http.Request) {
	c.restoreMu.Lock()
	defer c.restoreMu.Unlock()
	if err := r.Context().Err(); err != nil {
		return
	}
	c.mu.Lock()
	limited := len(c.downloads) >= 64
	c.mu.Unlock()
	if limited {
		http.Error(w, "cache download limit", http.StatusTooManyRequests)
		return
	}
	keys := strings.Split(r.URL.Query().Get("keys"), ",")
	version := r.URL.Query().Get("version")
	if len(keys) == 0 || len(keys) > 33 || !cacheVersion(version) {
		http.Error(w, "invalid cache lookup", http.StatusBadRequest)
		return
	}
	for _, key := range keys {
		if len(key) == 0 || len(key) > 512 {
			http.Error(w, "invalid cache key", http.StatusBadRequest)
			return
		}
	}
	c.mu.Lock()
	c.next++
	id := c.next
	c.mu.Unlock()
	archive := filepath.Join(c.dir, fmt.Sprint(id)+".tar")
	entry, err := c.services.Download(r.Context(), "cache", keys[0], version, keys[1:], false, archive)
	if errors.Is(err, blob.ErrNotFound) {
		w.WriteHeader(204)
		return
	}
	if err != nil {
		http.Error(w, "cache storage unavailable", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = os.Remove(archive) }()
	c.mu.Lock()
	defer c.mu.Unlock()
	info, statErr := os.Stat(archive)
	if statErr != nil || c.stagedBytes()+info.Size() > c.stageLimit() {
		http.Error(w, "cache staging quota exceeded", http.StatusRequestEntityTooLarge)
		return
	}
	file, err := unwrapCache(archive, filepath.Join(c.dir, fmt.Sprint(id)+".cache"), c.objectLimit())
	if err != nil {
		http.Error(w, "invalid stored cache", http.StatusInternalServerError)
		return
	}
	c.downloads[id] = file
	c.downloadBytes += info.Size()
	cacheJSON(w, 200, map[string]any{"cacheKey": entry.Key, "scope": "forgelet-job", "cacheVersion": version, "archiveLocation": c.base + "archive/" + fmt.Sprint(id)})
}
func (c *actCache) reserve(w http.ResponseWriter, r *http.Request) {
	var in struct{ Key, Version string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || len(in.Key) == 0 || len(in.Key) > 512 || !cacheVersion(in.Version) {
		http.Error(w, "invalid reservation", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.uploads) >= 4 {
		http.Error(w, "cache upload limit", http.StatusTooManyRequests)
		return
	}
	c.next++
	id := c.next
	name := filepath.Join(c.dir, fmt.Sprint(id)+".upload")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
		return
	}
	_ = f.Close()
	c.uploads[id] = &cacheUpload{Key: in.Key, Version: in.Version, File: name, Ranges: map[int64]int64{}}
	cacheJSON(w, 201, map[string]int{"cacheId": id})
}
func (c *actCache) upload(w http.ResponseWriter, r *http.Request, key string) {
	id, err := strconv.Atoi(key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	u := c.uploads[id]
	if u == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPatch {
		if u.Committed {
			http.Error(w, "cache already committed", http.StatusConflict)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/*", &start, &end); err != nil || start < 0 || end < start || end >= c.objectLimit()-2048 || end-start >= 32<<20 {
			http.Error(w, "invalid upload range", http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, end-start+2))
		if err != nil || int64(len(data)) != end-start+1 {
			http.Error(w, "invalid upload length", http.StatusBadRequest)
			return
		}
		for offset, length := range u.Ranges {
			if start < offset+length && end >= offset {
				if offset == start && length == int64(len(data)) {
					f, err := os.Open(u.File)
					if err != nil {
						http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
						return
					}
					previous := make([]byte, len(data))
					_, err = f.ReadAt(previous, start)
					_ = f.Close()
					if err == nil && bytes.Equal(previous, data) {
						w.WriteHeader(204)
						return
					}
				}
				http.Error(w, "overlapping upload range", http.StatusConflict)
				return
			}
		}
		if c.stagedBytes()+int64(len(data)) > c.stageLimit() {
			http.Error(w, "cache staging quota exceeded", http.StatusRequestEntityTooLarge)
			return
		}
		f, err := os.OpenFile(u.File, os.O_WRONLY, 0600)
		if err != nil {
			http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
			return
		}
		n, err := f.WriteAt(data, start)
		err = errors.Join(err, f.Close())
		if err != nil || n != len(data) {
			http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
			return
		}

		u.Ranges[start] = int64(n)
		u.Staged += int64(n)
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var in struct{ Size int64 }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&in); err != nil || in.Size <= 0 || in.Size > c.objectLimit()-2048 {
		http.Error(w, "invalid cache size", http.StatusBadRequest)
		return
	}
	if u.Committed {
		if in.Size != u.Size {
			http.Error(w, "cache commit size changed", http.StatusConflict)
			return
		}
		w.WriteHeader(204)
		return
	}
	offsets := make([]int64, 0, len(u.Ranges))
	for offset := range u.Ranges {
		offsets = append(offsets, offset)
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	var total int64
	for _, offset := range offsets {
		if offset != total {
			http.Error(w, "cache upload is incomplete", http.StatusBadRequest)
			return
		}
		total += u.Ranges[offset]
	}
	if total != in.Size {
		http.Error(w, "cache upload is incomplete", http.StatusBadRequest)
		return
	}
	archive := u.File + ".tar"
	if err := wrapCache(u.File, archive); err != nil {
		http.Error(w, "cache archive failed", http.StatusInternalServerError)
		return
	}
	defer func() { _ = os.Remove(archive) }()
	if _, err := c.services.Upload(r.Context(), "cache", u.Key, u.Version, 7, archive); err != nil {
		http.Error(w, "cache storage unavailable", http.StatusServiceUnavailable)
		return
	}
	u.Committed = true
	u.Size = in.Size
	w.WriteHeader(204)
}
func wrapCache(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	writer := tar.NewWriter(out)
	err = writer.WriteHeader(&tar.Header{Name: "cache", Mode: 0600, Size: info.Size(), Typeflag: tar.TypeReg})
	if err == nil {
		_, err = io.Copy(writer, in)
	}
	return errors.Join(err, writer.Close(), out.Close())
}
func unwrapCache(source, target string, objectLimit int64) (string, error) {
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	reader := tar.NewReader(in)
	header, err := reader.Next()
	if err != nil || header.Name != "cache" || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > objectLimit-2048 {
		return "", fmt.Errorf("invalid cache envelope")
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, err = io.CopyN(out, reader, header.Size)
	err = errors.Join(err, out.Close())
	if err != nil {
		return "", err
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("unexpected cache archive entry")
	}
	return target, nil
}

func (c *actCache) objectLimit() int64 {
	if c.objectBytes == 0 {
		return blob.MaxArchive
	}
	return c.objectBytes
}
func (c *actCache) stageLimit() int64 { return 2 * c.objectLimit() }
func (c *actCache) blockLimit() int   { return int(max(int64(maxCacheBlocks), c.objectLimit()/(1<<20))) }
