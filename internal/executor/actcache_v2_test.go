package executor

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/blob"
)

func TestActCacheV2LargeBlockLists(t *testing.T) {
	for _, budget := range []int64{blob.MaxArchive, 512 << 20, blob.MaxCacheArchive} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			c, err := startActCacheAt(t.Context(), &localServices{}, "127.0.0.1", budget)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			u := &cacheUpload{File: filepath.Join(c.dir, "blocks"), Blocks: map[string]string{}, BlockDigests: map[string][32]byte{}}
			c.uploads[1] = u
			put := func(query, body string, want int) {
				t.Helper()
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, c.URL()+"blob/1?"+query, strings.NewReader(body))
				res := httptest.NewRecorder()
				c.ServeHTTP(res, req)
				if res.Code != want {
					t.Fatalf("PUT: %d: %s", res.Code, res.Body.String())
				}
			}
			var list strings.Builder
			list.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?><BlockList>")
			for i := range c.blockLimit() {
				id := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%064d", i)))
				put("comp=block&blockid="+url.QueryEscape(id), "x", http.StatusCreated)
				fmt.Fprintf(&list, "<Latest>%s</Latest>", id)
			}
			list.WriteString("</BlockList>")
			if budget > blob.MaxArchive && list.Len() <= 32<<10 {
				t.Fatal("fixture does not cross the old XML body limit")
			}
			// A complete legal block list must fit even with maximum-length IDs.
			put("comp=blocklist", list.String(), http.StatusCreated)
			put("comp=blocklist", list.String(), http.StatusCreated)
			data, err := os.ReadFile(u.File)
			if err != nil || string(data) != strings.Repeat("x", c.blockLimit()) {
				t.Fatal("assembled payload", len(data), err)
			}
			// Count and byte ceilings remain enforced independently of payload size.
			put("comp=blocklist", strings.Replace(list.String(), "</BlockList>", "<Latest>eA==</Latest></BlockList>", 1), http.StatusBadRequest)
			put("comp=blocklist", strings.Repeat(" ", 256<<10)+list.String(), http.StatusBadRequest)
		})
	}
}

func TestActCacheExplicitLargerObjectKeepsSeparateLimits(t *testing.T) {
	c, err := startActCacheAt(t.Context(), &localServices{}, "127.0.0.1", blob.MaxCacheArchive)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.stageLimit() != 2*blob.MaxCacheArchive || c.blockLimit() != 1024 {
		t.Fatal("larger cache has no bounded staging/block budget")
	}
	u := &cacheUpload{File: filepath.Join(c.dir, "large"), Staged: blob.MaxArchive, Blocks: map[string]string{}, BlockDigests: map[string][32]byte{}}
	c.uploads[1] = u
	id := base64.StdEncoding.EncodeToString([]byte("0001"))
	put := func(want int, message string) {
		req := httptest.NewRequestWithContext(t.Context(), "PUT", c.URL()+"blob/1?comp=block&blockid="+id, strings.NewReader("x"))
		res := httptest.NewRecorder()
		c.azureUpload(res, req, "1")
		if res.Code != want || !strings.Contains(res.Body.String(), message) {
			t.Fatal(res.Code, res.Body.String())
		}
	}
	put(201, "") // Configuration, rather than the former 256 MiB constant, governs admission.
	u.Staged = blob.MaxCacheArchive - 2048
	u.Blocks = map[string]string{}
	u.BlockDigests = map[string][32]byte{}
	put(413, "object size exceeded")
	u.Staged = 0
	c.downloadBytes = c.stageLimit()
	put(413, "staging quota exceeded")
	if _, err := startActCacheAt(t.Context(), &localServices{}, "127.0.0.1", blob.MaxCacheArchive+1); err == nil {
		t.Fatal("accepted unbounded cache")
	}
}

func TestActCacheV2SDK64MiBBlockBoundary(t *testing.T) {
	c, err := startActCache(t.Context(), &localServices{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.uploads[1] = &cacheUpload{File: filepath.Join(c.dir, "sdk"), Blocks: map[string]string{}, BlockDigests: map[string][32]byte{}}
	block := base64.StdEncoding.EncodeToString([]byte("0001"))
	body := bytes.Repeat([]byte{'x'}, maxCacheBlockBytes+1)
	for _, tc := range []struct {
		name string
		size int
		want int
	}{
		{"empty", 0, 400},
		{"oversized", maxCacheBlockBytes + 1, 413},
		{"sdk chunk", maxCacheBlockBytes, 201},
		{"idempotent retry", maxCacheBlockBytes, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, c.URL()+"upload/1?comp=block&blockid="+block, bytes.NewReader(body[:tc.size]))
			res := httptest.NewRecorder()
			c.azureUpload(res, req, "1")
			if res.Code != tc.want {
				t.Fatalf("status %d: %s", res.Code, res.Body.String())
			}
			wantBytes := int64(0)
			if tc.want == 201 {
				wantBytes = maxCacheBlockBytes
			}
			if c.uploads[1].Staged != wantBytes {
				t.Fatalf("rejected upload/retry changed quota: %d", c.uploads[1].Staged)
			}
		})
	}
}

func TestActCacheV2BlocksPersistenceCapabilityAndRange(t *testing.T) {
	store, err := blob.Open(t.TempDir(), blob.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	services := &localServices{store: store}
	cache, err := startActCache(t.Context(), services, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	request := func(method, url, body, auth string, headers map[string]string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	version := strings.Repeat("a", 64)
	rpc := func(c *actCache, method, body string) map[string]any {
		t.Helper()
		status, data := request("POST", "http://"+c.listener.Addr().String()+"/"+cacheServicePath+method, body, c.token, nil)
		if status != 200 {
			t.Fatalf("%s: %d %s", method, status, data)
		}
		var out map[string]any
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	lookup := fmt.Sprintf(`{"key":"layer","version":%q}`, version)
	if rpc(cache, "GetCacheEntryDownloadURL", lookup)["ok"] != false {
		t.Fatal("cold cache hit")
	}
	upload := rpc(cache, "CreateCacheEntry", lookup)["signed_upload_url"].(string)
	first := base64.StdEncoding.EncodeToString([]byte("0001"))
	second := base64.StdEncoding.EncodeToString([]byte("0002"))
	for _, block := range []struct{ id, body string }{{second, "cache"}, {first, "opaque-"}, {first, "opaque-"}} {
		if status, data := request("PUT", upload+"?comp=block&blockid="+block.id, block.body, "", nil); status != 201 {
			t.Fatal(status, string(data))
		}
	}
	if status, _ := request("PUT", upload+"?comp=block&blockid="+first, "changed", "", nil); status != 409 {
		t.Fatal("changed retry", status)
	}
	list := "<BlockList><Latest>" + first + "</Latest><Latest>" + second + "</Latest></BlockList>"
	for range 2 {
		if status, data := request("PUT", upload+"?comp=blocklist", list, "", nil); status != 201 {
			t.Fatal(status, string(data))
		}
	}
	for range 2 {
		if rpc(cache, "FinalizeCacheEntryUpload", fmt.Sprintf(`{"key":"layer","version":%q,"size_bytes":"12"}`, version))["ok"] != true {
			t.Fatal("finalize")
		}
	}
	if services.uploads != 1 {
		t.Fatal("duplicate finalize persisted twice", services.uploads)
	}
	// A fresh attempt has a different capability; storage is the only shared state.
	fresh, err := startActCache(t.Context(), services, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	hit := rpc(fresh, "GetCacheEntryDownloadURL", lookup)
	if hit["ok"] != true || hit["matched_key"] != "layer" {
		t.Fatal(hit)
	}
	location := hit["signed_download_url"].(string)
	fresh.mu.Lock()
	beforeBytes := fresh.downloadBytes
	fresh.mu.Unlock()
	for range 4 {
		if repeated := rpc(fresh, "GetCacheEntryDownloadURL", lookup); repeated["signed_download_url"] != location {
			t.Fatal("exact immutable restore allocated another file", repeated)
		}
	}
	fresh.mu.Lock()
	afterBytes, files := fresh.downloadBytes, len(fresh.downloads)
	fresh.mu.Unlock()
	if afterBytes != beforeBytes || files != 1 {
		t.Fatal("repeated lookup consumed staging quota", beforeBytes, afterBytes, files)
	}
	fallback := fmt.Sprintf(`{"key":"primary-later","version":%q,"restore_keys":["layer"]}`, version)
	if restored := rpc(fresh, "GetCacheEntryDownloadURL", fallback); restored["signed_download_url"] != location || restored["matched_key"] != "layer" {
		t.Fatal("fallback did not reuse the matched immutable object", restored)
	}
	primaryLookup := fmt.Sprintf(`{"key":"primary-later","version":%q}`, version)
	primaryUpload := rpc(cache, "CreateCacheEntry", primaryLookup)["signed_upload_url"].(string)
	if status, data := request("PUT", primaryUpload, "new-cache", "", nil); status != 201 {
		t.Fatal(status, string(data))
	}
	rpc(cache, "FinalizeCacheEntryUpload", fmt.Sprintf(`{"key":"primary-later","version":%q,"size_bytes":9}`, version))
	if restored := rpc(fresh, "GetCacheEntryDownloadURL", fallback); restored["matched_key"] != "primary-later" || restored["signed_download_url"] == location {
		t.Fatal("fallback lookup hid a newly saved primary entry", restored)
	}
	status, data := request("GET", location, "", "", map[string]string{"x-ms-range": "bytes=7-11"})
	if status != 206 || string(data) != "cache" {
		t.Fatal(status, string(data))
	}
	if status, _ := request("POST", "http://"+fresh.listener.Addr().String()+"/"+cacheServicePath+"CreateCacheEntry", lookup, cache.token, nil); status != 401 {
		t.Fatal("cross-job token accepted", status)
	}
	if status, _ := request("PUT", strings.Replace(upload, cache.capability, "wrong", 1), "bad", "", nil); status != 404 {
		t.Fatal("unsigned capability accepted", status)
	}
	if status, _ := request("POST", fresh.URL()+cacheServicePath+"FinalizeCacheEntryUpload", fmt.Sprintf(`{"key":"layer","version":%q,"size_bytes":12}`, version), fresh.token, nil); status != 404 {
		t.Fatal("unreserved finalization accepted", status)
	}
	if bytes.Contains(data, []byte(cache.token)) {
		t.Fatal("credential in archive")
	}
}

func TestActCacheV2RejectsIncompleteUnsupportedAndChangedUploads(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{{"incomplete", `{"size_bytes":1}`, 400}, {"malformed", "!", 400}, {"unknown", `{}`, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := blob.Open(t.TempDir(), blob.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			c, err := startActCache(t.Context(), &localServices{store: store}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			req, err := http.NewRequestWithContext(t.Context(), "POST", c.URL()+cacheServicePath+"FinalizeCacheEntryUpload", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+c.token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatal(resp.StatusCode)
			}
		})
	}
}

func TestActCacheV2SingleBlobFinalizationAndQuota(t *testing.T) {
	store, err := blob.Open(t.TempDir(), blob.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	c, err := startActCache(t.Context(), &localServices{store: store}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, c.URL()+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, data)
		}
		var result map[string]any
		if want == 200 {
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	version := strings.Repeat("b", 64)
	body := fmt.Sprintf(`{"key":"single","version":%q}`, version)
	created := request("POST", cacheServicePath+"CreateCacheEntry", body, 200)
	path := strings.TrimPrefix(created["signed_upload_url"].(string), c.URL())
	request("POST", cacheServicePath+"FinalizeCacheEntryUpload", fmt.Sprintf(`{"key":"single","version":%q,"size_bytes":3}`, version), 400)
	request("PUT", path+"?comp=blocklist", "<BlockList><Latest>missing</Latest></BlockList>", 400)
	block := base64.StdEncoding.EncodeToString([]byte("old"))
	request("PUT", path+"?comp=block&blockid="+block, "uncommitted", 201)
	request("PUT", path, "one", 201)
	request("PUT", path, "one", 201)
	if files, err := filepath.Glob(filepath.Join(c.dir, "*-block-*")); err != nil || len(files) != 0 {
		t.Fatalf("replaced blocks retained on disk: %v %v", files, err)
	}
	c.mu.Lock()
	staged := c.stagedBytes()
	c.mu.Unlock()
	if staged != 3 {
		t.Fatalf("replacement staging size = %d, want 3", staged)
	}
	request("PUT", path+"?comp=block&blockid="+block, "uncommitted", 409)
	request("PUT", path, "two", 409)
	request("POST", cacheServicePath+"FinalizeCacheEntryUpload", fmt.Sprintf(`{"key":"single","version":%q,"size_bytes":2}`, version), 400)
	request("POST", cacheServicePath+"FinalizeCacheEntryUpload", fmt.Sprintf(`{"key":"single","version":%q,"size_bytes":3}`, version), 200)
	wrong := fmt.Sprintf(`{"key":"single","version":%q}`, strings.Repeat("c", 64))
	if request("POST", cacheServicePath+"GetCacheEntryDownloadURL", wrong, 200)["ok"] != false {
		t.Fatal("cross-version hit")
	}
	created = request("POST", cacheServicePath+"CreateCacheEntry", fmt.Sprintf(`{"key":"quota","version":%q}`, version), 200)
	c.mu.Lock()
	c.downloadBytes = cacheStageLimit
	c.mu.Unlock()
	request("PUT", strings.TrimPrefix(created["signed_upload_url"].(string), c.URL()), "x", 413)
	if parts := strings.Split(c.token, "."); len(parts) != 3 {
		t.Fatal("BuildKit runtime token is not a JWT")
	}
}

func TestActCacheV2SmallBlocksBeyond128AndDistinctLimits(t *testing.T) {
	store, err := blob.Open(t.TempDir(), blob.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	c, err := startActCache(t.Context(), &localServices{store: store}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	request := func(method, target, body string, want int, message string) map[string]any {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want || !strings.Contains(string(data), message) {
			t.Fatalf("%d: %s", response.StatusCode, data)
		}
		var result map[string]any
		if want == 200 {
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	version := strings.Repeat("d", 64)
	reserve := func(key string) string {
		t.Helper()
		return request("POST", c.URL()+cacheServicePath+"CreateCacheEntry", fmt.Sprintf(`{"key":%q,"version":%q}`, key, version), 200, "")["signed_upload_url"].(string)
	}
	upload := reserve("many-small-blocks")
	// Tiny payloads isolate the client's block-count requirement from byte quotas.
	var list strings.Builder
	list.WriteString("<BlockList>")
	for i := 0; i < 256; i++ {
		id := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%04d", i)))
		request("PUT", upload+"?comp=block&blockid="+id, "x", 201, "")
		fmt.Fprintf(&list, "<Latest>%s</Latest>", id)
	}
	list.WriteString("</BlockList>")
	extra := base64.StdEncoding.EncodeToString([]byte("0256"))
	request("PUT", upload+"?comp=block&blockid="+extra, "x", 413, "block count exceeded")
	request("PUT", upload+"?comp=blocklist", list.String(), 201, "")
	request("POST", c.URL()+cacheServicePath+"FinalizeCacheEntryUpload", fmt.Sprintf(`{"key":"many-small-blocks","version":%q,"size_bytes":256}`, version), 200, "")
	object := reserve("object-bound")
	c.mu.Lock()
	for _, u := range c.uploads {
		if u.Key == "object-bound" {
			u.Staged = blob.MaxArchive - 2048
		}
	}
	c.mu.Unlock()
	request("PUT", object+"?comp=block&blockid="+extra, "x", 413, "object size exceeded")
	quota := reserve("stage-bound")
	c.mu.Lock()
	c.downloadBytes = cacheStageLimit
	c.mu.Unlock()
	request("PUT", quota+"?comp=block&blockid="+extra, "x", 413, "staging quota exceeded")
}
