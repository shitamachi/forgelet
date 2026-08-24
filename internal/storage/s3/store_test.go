package s3

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
)

func testS3(t *testing.T) (*Store, bool) {
	t.Helper()
	endpoint := os.Getenv("FORGELET_TEST_S3")
	if endpoint == "" {
		t.Skip("FORGELET_TEST_S3 not set; skipping S3 integration")
		return nil, false
	}
	access := os.Getenv("FORGELET_TEST_S3_ACCESS")
	if access == "" {
		access = "minioadmin"
	}
	secret := os.Getenv("FORGELET_TEST_S3_SECRET")
	if secret == "" {
		secret = "minioadmin"
	}
	bucket := os.Getenv("FORGELET_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "forgelet-test"
	}
	s, err := New(endpoint, access, secret, bucket, false)
	if err != nil {
		t.Fatalf("s3 new: %v", err)
	}
	ctx := context.Background()
	if err := s.EnsureBucket(ctx); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	return s, true
}

func TestS3CacheExactAndRestore(t *testing.T) {
	s, _ := testS3(t)
	ctx := context.Background()
	repo := model.RepositoryRef{Provider: "github", Owner: "o", Name: "r"}
	repo2 := model.RepositoryRef{Provider: "github", Owner: "o", Name: "other"}

	key := "my-cache-key-" + time.Now().Format("150405.000000")
	restore := "my-cache-key-"

	hit, _, err := s.CacheResolve(ctx, repo, key, []string{restore})
	if err != nil {
		t.Fatalf("resolve miss: %v", err)
	}
	if hit {
		t.Fatal("expected miss")
	}
	putURL, err := s.CachePutURL(ctx, repo, key)
	if err != nil {
		t.Fatalf("put url: %v", err)
	}
	if err := putBytes(ctx, putURL, []byte("cache-data")); err != nil {
		t.Fatalf("put: %v", err)
	}
	hit, hitKey, err := s.CacheResolve(ctx, repo, key, nil)
	if err != nil || !hit {
		t.Fatalf("exact hit: %v hit=%v", err, hit)
	}
	getURL, err := s.CacheGetURL(ctx, hitKey)
	if err != nil {
		t.Fatalf("get url: %v", err)
	}
	data, err := getBytes(ctx, getURL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(data) != "cache-data" {
		t.Errorf("data = %q", string(data))
	}
	otherKey := "my-cache-key-other-" + time.Now().Format("150405.000001")
	hit, _, err = s.CacheResolve(ctx, repo, otherKey, []string{restore})
	if err != nil {
		t.Fatalf("restore hit: %v", err)
	}
	if !hit {
		t.Error("restore-keys prefix should hit")
	}
	hit, _, err = s.CacheResolve(ctx, repo2, key, nil)
	if err != nil {
		t.Fatalf("other repo: %v", err)
	}
	if hit {
		t.Error("other repo should not hit")
	}
}

func TestS3ArtifactRoundTrip(t *testing.T) {
	s, _ := testS3(t)
	ctx := context.Background()
	repo := model.RepositoryRef{Provider: "github", Owner: "o", Name: "r"}
	runID := model.RunID("01JTEST0000000000000000000X")

	name := "my-artifact-" + time.Now().Format("150405.000")
	putURL, err := s.ArtifactPutURL(ctx, repo, runID, name)
	if err != nil {
		t.Fatalf("put url: %v", err)
	}
	if err := putBytes(ctx, putURL, []byte("artifact-data")); err != nil {
		t.Fatalf("put: %v", err)
	}
	getURL, err := s.ArtifactGetURL(ctx, repo, runID, name)
	if err != nil {
		t.Fatalf("get url: %v", err)
	}
	data, err := getBytes(ctx, getURL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(data) != "artifact-data" {
		t.Errorf("data = %q", string(data))
	}
}

func putBytes(ctx context.Context, url string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpError{Status: resp.StatusCode}
	}
	return nil
}

func getBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpError{Status: resp.StatusCode}
	}
	return io.ReadAll(resp.Body)
}

type httpError struct{ Status int }

func (e *httpError) Error() string { return "http status " + http.StatusText(e.Status) }
