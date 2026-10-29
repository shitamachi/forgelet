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

// Only explicit source/authorization admission 503s before persistence are safe to retry. Transport
// errors and all other mutation responses retain the normal single-request rule.
func createPublicWorkflow(t *testing.T, ctx context.Context, client *http.Client, endpoint string, body any) run.Run {
	t.Helper()
	encoded, err := json.Marshal(body)
	must(t, err)
	sourceRetries, authorizationRetries := 0, 0
	for {
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
		if response.StatusCode != 503 {
			t.Fatalf("create public workflow: HTTP %d: %s", response.StatusCode, data)
		}
		switch {
		case strings.Contains(string(data), "public Action source temporarily unavailable") && sourceRetries < 2:
			sourceRetries++
			t.Logf("public Action source unavailable before persistence; retry admission %d/3", sourceRetries+1)
		case strings.HasPrefix(string(data), "GitHub repository authorization pending:") && authorizationRetries < 30:
			authorizationRetries++
			if authorizationRetries == 1 {
				t.Log("waiting for post-restart GitHub authorization confirmation before admission")
			}
		default:
			t.Fatalf("create public workflow: HTTP %d: %s", response.StatusCode, data)
		}

		select {
		case <-ctx.Done():
			must(t, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
