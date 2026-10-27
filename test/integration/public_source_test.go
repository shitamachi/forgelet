//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

// Only an explicit admission 503 before persistence is safe to retry. Transport
// errors and all other mutation responses retain the normal single-request rule.
func createPublicWorkflow(t *testing.T, ctx context.Context, client *http.Client, endpoint string, body any) run.Run {
	t.Helper()
	encoded, err := json.Marshal(body)
	must(t, err)
	for attempt := range 3 {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/runs", bytes.NewReader(encoded))
		must(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		must(t, err)
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		_ = response.Body.Close()
		must(t, readErr)
		if response.StatusCode == 201 {
			var item run.Run
			must(t, json.Unmarshal(data, &item))
			return item
		}
		if response.StatusCode != 503 || !strings.Contains(string(data), "public Action source temporarily unavailable") || attempt == 2 {
			t.Fatalf("create public workflow: HTTP %d: %s", response.StatusCode, data)
		}
		t.Logf("public Action source unavailable before persistence; retry admission %d/3", attempt+2)
		select {
		case <-ctx.Done():
			must(t, ctx.Err())
		case <-time.After(time.Second):
		}
	}
	t.Fatal("public source retry exhausted")
	return run.Run{}
}
