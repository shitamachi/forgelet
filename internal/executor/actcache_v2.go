package executor

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/blob"
)

const cacheStageLimit = 512 << 20
const maxCacheBlocks = 256

// setup-go's pinned cache SDK uploads Azure blocks in 64 MiB chunks.
const maxCacheBlockBytes = 64 << 20

const cacheServicePath = "twirp/github.actions.results.api.v1.CacheService/"

// BuildKit reads these claims before making requests. Authorization is still an
// exact per-attempt capability comparison, never trust in client-supplied claims.
func cacheRuntimeToken(key []byte, now time.Time) string {
	encode := base64.RawURLEncoding.EncodeToString
	header := encode([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(24 * time.Hour).Unix(), "ac": `[{"Scope":"forgelet-job","Permission":3}]`})
	payload := header + "." + encode(claims)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + encode(mac.Sum(nil))
}
func serveCacheFile(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("x-ms-blob-type", "BlockBlob")
	if value := r.Header.Get("x-ms-range"); value != "" {
		r.Header.Set("Range", value)
	}
	http.ServeFile(w, r, name)
}

type cacheV2Request struct {
	Key         string      `json:"key"`
	Version     string      `json:"version"`
	RestoreKeys []string    `json:"restore_keys"`
	Size        json.Number `json:"size_bytes"`
}

func (c *actCache) cacheV2(w http.ResponseWriter, r *http.Request, method string) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var in cacheV2Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in); err != nil || !cacheVersion(in.Version) || !validCacheKey(in.Key) || len(in.RestoreKeys) > 32 {
		http.Error(w, "invalid cache request", http.StatusBadRequest)
		return
	}
	for _, key := range in.RestoreKeys {
		if !validCacheKey(key) {
			http.Error(w, "invalid restore key", http.StatusBadRequest)
			return
		}
	}
	switch method {
	case "GetCacheEntryDownloadURL":
		c.restoreV2(w, r, in)
	case "CreateCacheEntry":
		c.mu.Lock()
		defer c.mu.Unlock()
		for id, u := range c.uploads {
			if u.Key == in.Key && u.Version == in.Version && u.Blocks != nil {
				cacheJSON(w, 200, map[string]any{"ok": !u.Committed, "signed_upload_url": c.base + "blob/" + strconv.Itoa(id)})
				return
			}
		}
		if len(c.uploads) >= 64 {
			http.Error(w, "cache upload limit", http.StatusTooManyRequests)
			return
		}
		c.next++
		id := c.next
		c.uploads[id] = &cacheUpload{Key: in.Key, Version: in.Version, File: filepath.Join(c.dir, fmt.Sprint(id)+".upload"), Blocks: map[string]string{}, BlockDigests: map[string][32]byte{}}
		cacheJSON(w, 200, map[string]any{"ok": true, "signed_upload_url": c.base + "blob/" + strconv.Itoa(id)})
	case "FinalizeCacheEntryUpload":
		size, err := in.Size.Int64()
		if err != nil || size <= 0 || size > c.objectLimit()-2048 {
			http.Error(w, "invalid cache size", http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		for id, u := range c.uploads {
			if u.Key != in.Key || u.Version != in.Version || u.Blocks == nil {
				continue
			}
			if !u.Sealed || size != u.Size {
				http.Error(w, "cache upload incomplete", http.StatusBadRequest)
				return
			}
			if !u.Committed {
				archive := u.File + ".tar"
				if err := wrapCache(u.File, archive); err != nil {
					http.Error(w, "cache envelope failed", http.StatusInternalServerError)
					return
				}
				defer func() { _ = os.Remove(archive) }()
				if _, err := c.services.Upload(r.Context(), "cache", u.Key, u.Version, 7, archive); err != nil {
					http.Error(w, "cache storage unavailable", http.StatusServiceUnavailable)
					return
				}
				u.Committed = true
			}
			cacheJSON(w, 200, map[string]any{"ok": true, "entry_id": strconv.Itoa(id)})
			return
		}
		http.Error(w, "unknown cache reservation", http.StatusNotFound)
	default:
		http.NotFound(w, r)
	}
}
func validCacheKey(key string) bool {
	return len(key) > 0 && len(key) <= 512 && !strings.ContainsAny(key, "\x00\r\n")
}
func (c *actCache) restoreV2(w http.ResponseWriter, r *http.Request, in cacheV2Request) {
	c.restoreMu.Lock()
	defer c.restoreMu.Unlock()
	if err := r.Context().Err(); err != nil {
		return
	}
	c.mu.Lock()
	id := c.exactDownloads[in.Version+"\x00"+in.Key]
	limited := len(c.downloads) >= 64
	c.mu.Unlock()
	// Exact immutable objects already restored by this attempt share one URL
	// and one staged file. BuildKit looks up layers again while exporting.
	if id != 0 {
		cacheJSON(w, 200, map[string]any{"ok": true, "matched_key": in.Key, "signed_download_url": c.base + "archive/" + strconv.Itoa(id)})
		return
	}
	if limited {
		http.Error(w, "cache download limit", http.StatusTooManyRequests)
		return
	}
	c.mu.Lock()
	c.next++
	id = c.next
	c.mu.Unlock()
	archive := filepath.Join(c.dir, fmt.Sprint(id)+".tar")
	entry, err := c.services.Download(r.Context(), "cache", in.Key, in.Version, in.RestoreKeys, false, archive)
	if errors.Is(err, blob.ErrNotFound) {
		cacheJSON(w, 200, map[string]any{"ok": false})
		return
	}
	if err != nil {
		http.Error(w, "cache storage unavailable", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = os.Remove(archive) }()
	c.mu.Lock()
	defer c.mu.Unlock()
	// A fallback query must still consult storage: a newly saved primary key
	// can supersede its earlier fallback. Only reuse the matched object itself.
	identity := in.Version + "\x00" + entry.Key
	if existing := c.exactDownloads[identity]; existing != 0 {
		cacheJSON(w, 200, map[string]any{"ok": true, "matched_key": entry.Key, "signed_download_url": c.base + "archive/" + strconv.Itoa(existing)})
		return
	}
	info, statErr := os.Stat(archive)
	if statErr != nil || c.stagedBytes()+info.Size() > c.stageLimit() {
		cacheJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"code": "resource_exhausted", "msg": "cache staging quota exceeded"})
		return
	}
	file, err := unwrapCache(archive, filepath.Join(c.dir, fmt.Sprint(id)+".cache"), c.objectLimit())
	if err != nil {
		http.Error(w, "invalid cache envelope", http.StatusInternalServerError)
		return
	}
	c.downloads[id] = file
	c.exactDownloads[identity] = id
	c.downloadBytes += info.Size()
	cacheJSON(w, 200, map[string]any{"ok": true, "matched_key": entry.Key, "signed_download_url": c.base + "archive/" + strconv.Itoa(id)})
}
func (c *actCache) azureUpload(w http.ResponseWriter, r *http.Request, key string) {
	id, err := strconv.Atoi(key)
	if err != nil || r.Method != http.MethodPut {
		http.NotFound(w, r)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	u := c.uploads[id]
	if u == nil || u.Blocks == nil {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Query().Get("comp") {
	case "block":
		blockID := r.URL.Query().Get("blockid")
		decoded, err := base64.StdEncoding.DecodeString(blockID)
		if err != nil || len(decoded) == 0 || len(decoded) > 64 {
			http.Error(w, "invalid block ID", http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCacheBlockBytes))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "cache block size exceeded (maximum 64 MiB)", http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil || len(data) == 0 {
			http.Error(w, "invalid block length", http.StatusBadRequest)
			return
		}
		digest := sha256.Sum256(data)
		if previous, ok := u.BlockDigests[blockID]; ok {
			if previous != digest {
				http.Error(w, "changed block retry", http.StatusConflict)
				return
			}
			azureCreated(w)
			return
		}

		if u.Sealed || u.Committed {
			http.Error(w, "cache already sealed", http.StatusConflict)
			return
		}
		if len(u.Blocks) >= c.blockLimit() {
			http.Error(w, fmt.Sprintf("cache block count exceeded (maximum %d)", c.blockLimit()), http.StatusRequestEntityTooLarge)
			return
		}
		if u.Staged+int64(len(data)) > c.objectLimit()-2048 {
			http.Error(w, "cache object size exceeded", http.StatusRequestEntityTooLarge)
			return
		}
		if c.stagedBytes()+int64(len(data)) > c.stageLimit() {
			http.Error(w, "cache staging quota exceeded", http.StatusRequestEntityTooLarge)
			return
		}
		name := u.File + "-block-" + strconv.Itoa(len(u.Blocks))
		if err := os.WriteFile(name, data, 0600); err != nil {
			http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
			return
		}
		u.Blocks[blockID] = name
		u.BlockDigests[blockID] = digest
		u.Staged += int64(len(data))
		azureCreated(w)
	case "blocklist":
		var list struct {
			Latest []string `xml:"Latest"`
		}
		if err := xml.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&list); err != nil || len(list.Latest) == 0 || len(list.Latest) > c.blockLimit() {
			http.Error(w, "invalid block list", http.StatusBadRequest)
			return
		}
		listDigest := sha256.Sum256([]byte(strings.Join(list.Latest, "\x00")))
		if u.Sealed {
			if listDigest != u.ListDigest {
				http.Error(w, "changed block list retry", http.StatusConflict)
				return
			}
			azureCreated(w)
			return
		}
		seen := map[string]bool{}
		// Copy one bounded block at a time; the final object is streamed to disk.
		temporary := u.File + ".assembling"
		out, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
			return
		}
		defer func() { _ = os.Remove(temporary) }()
		var size int64
		for _, blockID := range list.Latest {
			name := u.Blocks[blockID]
			if name == "" || seen[blockID] {
				_ = out.Close()
				http.Error(w, "missing or duplicate block", http.StatusBadRequest)
				return
			}
			seen[blockID] = true
			file, err := os.Open(name)
			if err != nil {
				_ = out.Close()
				http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
				return
			}
			n, err := io.Copy(out, file)
			_ = file.Close()
			size += n
			if err != nil {
				_ = out.Close()
				http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
				return
			}
		}
		if err := out.Close(); err != nil {
			http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
			return
		}
		if err := os.Rename(temporary, u.File); err != nil {
			http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
			return
		}
		u.Sealed = true
		u.Size = size
		u.Staged = size
		u.ListDigest = listDigest
		for _, name := range u.Blocks {
			_ = os.Remove(name)
		}
		azureCreated(w)
	case "":
		// SDKs use a single Put Blob for archives as large as the object quota.
		// Stream to disk rather than allocating the entire archive in memory.
		temporary := u.File + ".put"
		out, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
			return
		}
		defer func() { _ = os.Remove(temporary) }()
		n, copyErr := io.Copy(out, http.MaxBytesReader(w, r.Body, c.objectLimit()-2048))
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || n == 0 {
			http.Error(w, "invalid blob length", http.StatusBadRequest)
			return
		}
		if u.Sealed {
			same, err := equalCacheFiles(u.File, temporary)
			if err != nil || !same {
				http.Error(w, "changed blob retry", http.StatusConflict)
				return
			}
			azureCreated(w)
			return
		}
		if c.stagedBytes()+n > c.stageLimit() {
			http.Error(w, "cache staging quota exceeded", http.StatusRequestEntityTooLarge)
			return
		}
		if err := os.Rename(temporary, u.File); err != nil {
			http.Error(w, "cache staging unavailable", http.StatusInternalServerError)
			return
		}
		// Put Blob replaces an uncommitted block upload. Do not retain its
		// old payload after replacing the staging accounting with the new size.
		for _, name := range u.Blocks {
			_ = os.Remove(name)
		}
		clear(u.Blocks)
		clear(u.BlockDigests)
		u.Sealed = true
		u.Size = n
		u.Staged = n
		azureCreated(w)
	default:
		http.NotFound(w, r)
	}
}
func (c *actCache) stagedBytes() int64 {
	size := c.downloadBytes
	for _, u := range c.uploads {
		size += u.Staged
	}
	return size
}
func equalCacheFiles(a, b string) (bool, error) {
	digest := func(name string) ([]byte, error) {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		h := sha256.New()
		_, err = io.Copy(h, f)
		return h.Sum(nil), err
	}
	first, err := digest(a)
	if err != nil {
		return false, err
	}
	second, err := digest(b)
	return bytes.Equal(first, second), err
}
func azureCreated(w http.ResponseWriter) {
	w.Header().Set("ETag", `"forgelet-cache"`)
	w.Header().Set("x-ms-request-id", "forgelet-job-cache")
	w.WriteHeader(http.StatusCreated)
}
