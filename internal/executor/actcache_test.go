package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/blob"
)

func TestActCacheRoundTripRetriesAndImmutableStorage(t *testing.T) {
	store, err := blob.Open(t.TempDir(), blob.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	services := &localServices{store: store}
	c, err := startActCache(t.Context(), services, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	client := &http.Client{}
	request := func(method, path string, body []byte, contentRange string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, c.URL()+"_apis/artifactcache/"+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Range", contentRange)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	version := strings.Repeat("a", 64)
	if code, _ := request("GET", "cache?keys=go-cold&version="+version, nil, ""); code != 204 {
		t.Fatal(code)
	}
	code, data := request("POST", "caches", []byte(fmt.Sprintf(`{"key":"go-cold","version":%q}`, version)), "")
	if code != 201 {
		t.Fatal(code, string(data))
	}
	var reservation struct {
		CacheID int `json:"cacheId"`
	}
	if err := json.Unmarshal(data, &reservation); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprint("caches/", reservation.CacheID)
	// A missing first range cannot be committed; an acknowledged chunk may retry.
	if code, _ := request("PATCH", path, []byte("cache"), "bytes 7-11/*"); code != 204 {
		t.Fatal(code)
	}
	if code, _ := request("POST", path, []byte(`{"size":12}`), ""); code != 400 {
		t.Fatal("accepted incomplete cache", code)
	}
	for range 2 {
		if code, _ := request("PATCH", path, []byte("opaque-"), "bytes 0-6/*"); code != 204 {
			t.Fatal("retry not idempotent", code)
		}
	}
	if code, _ := request("PATCH", path, []byte("changed"), "bytes 0-6/*"); code != 409 {
		t.Fatal("changed retry accepted", code)
	}
	for range 2 {
		if code, _ := request("POST", path, []byte(`{"size":12}`), ""); code != 204 {
			t.Fatal("commit", code)
		}
	}
	if services.uploads != 1 {
		t.Fatal("duplicate commit uploaded again")
	}
	code, data = request("GET", "cache?keys=go-cold&version="+version, nil, "")
	if code != 200 {
		t.Fatal(code, string(data))
	}
	var hit struct {
		Location string `json:"archiveLocation"`
		Key      string `json:"cacheKey"`
	}
	if err := json.Unmarshal(data, &hit); err != nil {
		t.Fatal(err)
	}
	if hit.Key != "go-cold" {
		t.Fatal(hit)
	}
	download, err := http.NewRequestWithContext(t.Context(), http.MethodGet, hit.Location, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(download)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(archive) != "opaque-cache" {
		t.Fatal(string(archive), err)
	}
	if code, _ := request("GET", "cache?keys=go-cold&version="+strings.Repeat("b", 64), nil, ""); code != 204 {
		t.Fatal("cross-version hit", code)
	}
	req, err := http.NewRequestWithContext(t.Context(), "POST", c.URL()+"_apis/artifactcache/caches", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("cache capability bypass")
	}
}
