package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// HTTPProjection reports observed phases to the control plane internal API
// (spec 0011 FR-D2). It implements DurableProjection without importing any
// storage adapter (module boundaries).
type HTTPProjection struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewHTTPProjection wires the client.
func NewHTTPProjection(baseURL, token string, hc *http.Client) *HTTPProjection {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &HTTPProjection{BaseURL: baseURL, Token: token, HTTP: hc}
}

// ApplyObserved implements DurableProjection.
func (p *HTTPProjection) ApplyObserved(ctx context.Context, id model.JobRunID, phase model.ObservedPhase, _ time.Time) error {
	body, err := json.Marshal(map[string]string{"phase": string(phase)})
	if err != nil {
		return fmt.Errorf("projection: encode: %w", err)
	}
	url := fmt.Sprintf("%s/internal/jobruns/%s/observed", p.BaseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("projection: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("projection: request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("projection: status %d", resp.StatusCode)
	}
	return nil
}

var _ DurableProjection = (*HTTPProjection)(nil)
