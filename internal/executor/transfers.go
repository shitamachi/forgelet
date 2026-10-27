package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/blob"
)

func (c *remoteClient) Upload(ctx context.Context, kind, key, version string, days int, filename string) (blob.Entry, error) {
	q := url.Values{"key": {key}, "version": {version}, "days": {strconv.Itoa(days)}}
	return c.transfer(ctx, http.MethodPut, kind, q, filename)
}
func (c *remoteClient) Download(ctx context.Context, kind, key, version string, prefixes []string, lookup bool, filename string) (blob.Entry, error) {
	q := url.Values{"key": {key}, "version": {version}, "restore-key": prefixes, "lookup": {strconv.FormatBool(lookup)}}
	return c.transfer(ctx, http.MethodGet, kind, q, filename)
}
func (c *remoteClient) Deploy(ctx context.Context, target, digest string) (string, error) {
	var response struct {
		ID string `json:"id"`
	}
	err := c.request(ctx, http.MethodPost, "/deployments", map[string]string{"target": target, "digest": digest}, &response)
	return response.ID, err
}
func (c *remoteClient) transfer(ctx context.Context, method, kind string, q url.Values, filename string) (blob.Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		e, err := c.transferOnce(ctx, method, kind, q, filename)
		if err == nil {
			return e, nil
		}
		var status *runtimeHTTPError
		if errors.As(err, &status) && status.status == 404 {
			return e, blob.ErrNotFound
		}
		if !errors.Is(err, errRuntimeTransport) && (!errors.As(err, &status) || status.status < 500) {
			return e, err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return e, ctx.Err()
		case <-timer.C:
		}
	}
}
func (c *remoteClient) transferOnce(ctx context.Context, method, kind string, q url.Values, filename string) (blob.Entry, error) {
	var entry blob.Entry
	var body io.Reader
	if method == http.MethodPut {
		f, err := os.Open(filename)
		if err != nil {
			return entry, err
		}
		defer func() { _ = f.Close() }()
		body = f
	}
	token, err := os.ReadFile(c.tokenFile)
	if err != nil || len(token) > 16384 {
		return entry, fmt.Errorf("cannot read workload token")
	}
	u := *c.base
	u.Path += "/blobs/" + kind
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return entry, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/x-tar")
	client := *c.client
	client.Timeout = 2 * time.Minute
	resp, err := client.Do(req)
	if err != nil {
		return entry, errRuntimeTransport
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return entry, &runtimeHTTPError{status: resp.StatusCode}
	}
	if method == http.MethodPut || q.Get("lookup") == "true" {
		data, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
		if err != nil {
			return entry, errRuntimeTransport
		}
		if len(data) > 8192 {
			return entry, fmt.Errorf("invalid blob response")
		}
		err = json.Unmarshal(data, &entry)
		return entry, err
	}
	f, err := os.Create(filename)
	if err != nil {
		return entry, err
	}
	h := sha256.New()
	limit := int64(blob.MaxArchive)
	if kind == "cache" {
		limit = blob.MaxCacheArchive
	}
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return entry, errRuntimeTransport
	}
	if n > limit {
		return entry, blob.ErrLimit
	}
	if hex.EncodeToString(h.Sum(nil)) != resp.Header.Get("X-Forgelet-Digest") {
		return entry, fmt.Errorf("blob digest mismatch")
	}
	entry.Key = resp.Header.Get("X-Forgelet-Key")
	entry.Size = n
	return entry, nil
}
