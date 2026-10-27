package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shitamachi/forgelet/internal/run"
)

// RemoteOptions configures the Pod's connection to the control plane. Projected
// workload tokens are read from TokenFile for every request, including retries.
type RemoteOptions struct {
	ActCacheObjectBytes int64
	URL                 string
	JobID               string
	PodUID              string
	TokenFile           string
	Workspace           string
	Client              *http.Client
	RetryTimeout        time.Duration
	PollInterval        time.Duration
	FlushTimeout        time.Duration
}

type remoteClient struct {
	base         *url.URL
	client       *http.Client
	tokenFile    string
	retryTimeout time.Duration
}

// RunRemote claims its one job, executes it and acknowledges logs and the final
// result. It never re-executes steps after a network error. A failed job returns
// an error after its terminal result has been acknowledged by the server.
func RunRemote(ctx context.Context, options RemoteOptions) error {
	client, err := newRemoteClient(options)
	if err != nil {
		return err
	}
	var plan run.Plan
	if err := client.request(ctx, http.MethodPost, "/claim", nil, &plan); err != nil {
		return fmt.Errorf("executor: claim: %w", err)
	}
	if plan.JobID != options.JobID || plan.RunID == "" {
		return errors.New("executor: claimed plan identity mismatch")
	}
	flushTimeout := options.FlushTimeout
	if flushTimeout <= 0 {
		flushTimeout = 20 * time.Second
	}
	finish := func(result run.Result) error {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
		defer cancel()
		return client.request(flushCtx, http.MethodPost, "/result", result, nil)
	}
	var credentials struct {
		CloneURL     string            `json:"clone_url"`
		PackageToken string            `json:"package_token"`
		CloneToken   string            `json:"clone_token"`
		Secrets      map[string]string `json:"secrets"`
	}
	needsCheckout := false
	for _, step := range plan.Steps {
		if step.Checkout != nil {
			needsCheckout = true
		}
	}
	if plan.Kernel == "act" && (plan.Permissions == nil || plan.Permissions["contents"] == "read") {
		needsCheckout = true
	}
	if needsCheckout || len(plan.SecretNames) > 0 {
		if err := client.request(ctx, http.MethodGet, "/credentials", nil, &credentials); err != nil {
			result := run.Result{State: run.Failed, Error: "clone credentials unavailable"}
			cancelled := ctx.Err() != nil
			if !cancelled {
				// Credentials are deliberately denied once cancellation starts. The Pod
				// can still read control state and acknowledge cancellation with its UID.
				checkCtx, cancelCheck := context.WithTimeout(context.WithoutCancel(ctx), min(flushTimeout, 5*time.Second))
				var control struct {
					CancelRequested bool `json:"cancel_requested"`
				}
				checkErr := client.request(checkCtx, http.MethodGet, "/control", nil, &control)
				cancelCheck()
				cancelled = checkErr == nil && control.CancelRequested || ctx.Err() != nil
			}
			if cancelled {
				result.State = run.Cancelled
				result.Error = ""
			}
			if reportErr := finish(result); reportErr != nil {
				return fmt.Errorf("executor: report credential failure: %w", reportErr)
			}
			if cancelled {
				return errRemoteCancellation
			}
			return fmt.Errorf("executor: credentials: %w", err)
		}
	}
	var mirrors map[string]string
	if value := os.Getenv("FORGELET_IMAGE_MIRRORS"); value != "" {
		if err := json.Unmarshal([]byte(value), &mirrors); err != nil {
			if err := finish(run.Result{State: run.Failed, Error: "invalid act image mirror configuration"}); err != nil {
				return err
			}
			return errors.New("executor: invalid act image mirror configuration")
		}
	}
	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	watchDone := make(chan error, 1)
	go func() { watchDone <- client.watchControl(sessionCtx, cancelSession, options.PollInterval) }()
	var logMu sync.Mutex
	uploadGate := make(chan struct{}, 1)
	var acknowledged int64
	var pending []run.Log
	var pendingBytes int
	var attempted int64
	appendLog := func(requestCtx context.Context, entry run.Log) error {
		var response struct {
			Seq int64 `json:"seq"`
		}
		if err := client.request(requestCtx, http.MethodPost, "/logs", entry, &response); err != nil {
			return err
		}
		if response.Seq != entry.Seq {
			return errors.New("executor: log acknowledgement mismatch")
		}
		return nil
	}
	flushLogs := func(requestCtx context.Context) error {
		// No producer may wait on network I/O while holding the queue lock.
		// Its own job/step context can then stop waiting for the uploader at
		// timeout, allowing Execute to return and cancel the in-flight request.
		logMu.Unlock()
		select {
		case uploadGate <- struct{}{}:
		case <-requestCtx.Done():
			logMu.Lock()
			return requestCtx.Err()
		}
		logMu.Lock()
		defer func() { <-uploadGate }()
		for len(pending) > 0 {
			entry := pending[0]
			attempted = entry.Seq
			logMu.Unlock()
			err := appendLog(requestCtx, entry)
			logMu.Lock()
			if err != nil {
				return err
			}
			acknowledged = entry.Seq
			pendingBytes -= len(pending[0].Data)
			pending[0] = run.Log{}
			pending = pending[1:]
		}
		return nil
	}
	log := func(logCtx context.Context, stepID string, data []byte) error {
		logMu.Lock()
		defer logMu.Unlock()
		if pendingBytes+len(data) > 1<<20 {
			return errors.New("executor: pending log buffer exhausted; output is incomplete")
		}
		// Coalesce only unsent data from the same step. Once attempted, a
		// sequence is immutable even if the server committed but its ACK was lost.
		for len(data) > 0 {
			last := len(pending) - 1
			if last < 0 || pending[last].Seq <= attempted || pending[last].StepID != stepID || len(pending[last].Data) == 8<<10 {
				pending = append(pending, run.Log{Seq: acknowledged + int64(len(pending)) + 1, StepID: stepID})
				last++
			}
			n := min(len(data), (8<<10)-len(pending[last].Data))
			for n > 0 && n < len(data) && !utf8.RuneStart(data[n]) {
				n--
			}
			if n == 0 {
				pending = append(pending, run.Log{Seq: acknowledged + int64(len(pending)) + 1, StepID: stepID})
				continue
			}
			pending[last].Data += string(data[:n])
			pendingBytes += n
			data = data[n:]
		}
		if logCtx.Err() != nil {
			return nil // Final bounded flush persists output after process shutdown.
		}
		if pendingBytes < 8<<10 {
			return nil
		}
		err := flushLogs(logCtx)
		if logCtx.Err() != nil {
			return nil
		}
		return err
	}
	// A small bounded delay preserves live logs from quiet/long-running steps;
	// large writes flush synchronously and retain the original backpressure.
	logDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				logDone <- nil
				return
			case <-ticker.C:
				logMu.Lock()
				err := flushLogs(sessionCtx)
				logMu.Unlock()
				if err != nil {
					if sessionCtx.Err() != nil {
						err = nil
					}
					cancelSession()
					logDone <- err
					return
				}
			}
		}
	}()
	result, executeErr := Execute(sessionCtx, plan, Options{ActCacheObjectBytes: options.ActCacheObjectBytes, BuildkitConfig: os.Getenv("FORGELET_BUILDKIT_CONFIG"), PackageToken: credentials.PackageToken, ActCacheHost: os.Getenv("FORGELET_ACT_CACHE_HOST"), ImageMirrors: mirrors, ActImage: os.Getenv("FORGELET_ACT_IMAGE"), BuildAddress: os.Getenv("FORGELET_BUILD_ADDR"), BuildImages: strings.Split(os.Getenv("FORGELET_BUILD_IMAGES"), "\n"), BuildInsecure: os.Getenv("FORGELET_BUILD_INSECURE") == "true", RegistryConfig: os.Getenv("FORGELET_REGISTRY_CONFIG"), Services: client, Workspace: options.Workspace, ContextLog: log, CloneURL: credentials.CloneURL, CloneToken: credentials.CloneToken, Secrets: credentials.Secrets})
	cancelSession()
	watchErr := <-watchDone
	logErr := <-logDone
	// Execution has stopped, so a bounded context can safely outlive SIGTERM or
	// cancellation long enough to persist a pending log and the terminal result.
	flushCtx, cancelFlush := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
	defer cancelFlush()
	logMu.Lock()
	if err := flushLogs(flushCtx); err != nil {
		logMu.Unlock()
		return fmt.Errorf("executor: flush log: %w", err)
	}
	result.LastLogSeq = acknowledged
	logMu.Unlock()
	if errors.Is(watchErr, errRemoteCancellation) {
		result.State = run.Cancelled
		result.Error = ""
		watchErr = nil
	}
	if ctx.Err() != nil {
		result.State = run.Cancelled
		result.Error = ""
	}
	if watchErr != nil {
		result.State = run.Failed
		result.Error = "control-plane cancellation status unavailable"
	}
	if logErr != nil {
		result.State = run.Failed
		result.Error = "control-plane log upload failed"
		executeErr = errors.Join(executeErr, logErr)
	}
	if !result.State.Terminal() {
		result.State = run.Failed
		result.Error = "executor returned a nonterminal result"
	}
	if errors.Is(executeErr, errActCleanup) {
		return fmt.Errorf("executor: waiting for Pod-owned engine termination: %w", executeErr)
	}
	if err := client.request(flushCtx, http.MethodPost, "/result", result, nil); err != nil {
		return fmt.Errorf("executor: report result: %w", err)
	}
	if watchErr != nil {
		return watchErr
	}
	if executeErr != nil {
		return fmt.Errorf("executor: execute: %w", executeErr)
	}
	if result.State != run.Succeeded {
		return fmt.Errorf("executor: job finished with state %s", result.State)
	}
	return nil
}

var errRemoteCancellation = errors.New("executor: cancellation requested")

func (c *remoteClient) watchControl(ctx context.Context, cancelExecution context.CancelFunc, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastSuccess := time.Now()
	for {
		pollCtx, cancelPoll := context.WithTimeout(ctx, 5*time.Second)
		var control struct {
			CancelRequested bool `json:"cancel_requested"`
		}
		err := c.requestOnce(pollCtx, http.MethodGet, "/control", nil, &control)
		cancelPoll()
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			lastSuccess = time.Now()
			if control.CancelRequested {
				cancelExecution()
				return errRemoteCancellation
			}
		} else {
			var status *runtimeHTTPError
			transient := errors.Is(err, errRuntimeTransport) || errors.As(err, &status) && status.status >= 500 && status.status <= 599
			if !transient || time.Since(lastSuccess) >= c.retryTimeout {
				cancelExecution()
				return errors.New("executor: cancellation polling failed")
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

var remoteIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func newRemoteClient(options RemoteOptions) (*remoteClient, error) {
	u, err := url.Parse(options.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("executor: invalid control-plane URL")
	}
	if !remoteIDPattern.MatchString(options.JobID) || options.TokenFile == "" || options.Workspace == "" {
		return nil, errors.New("executor: job ID, token file and workspace are required")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/internal/jobs/" + options.JobID
	httpClient := &http.Client{Timeout: 15 * time.Second}
	if options.Client != nil {
		copyClient := *options.Client
		httpClient = &copyClient
		if httpClient.Timeout == 0 {
			httpClient.Timeout = 15 * time.Second
		}
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	retryTimeout := options.RetryTimeout
	if retryTimeout <= 0 {
		retryTimeout = 5 * time.Minute
	}
	return &remoteClient{base: u, client: httpClient, tokenFile: options.TokenFile, retryTimeout: retryTimeout}, nil
}

type runtimeHTTPError struct{ status int }

func (e *runtimeHTTPError) Error() string { return fmt.Sprintf("control-plane HTTP %d", e.status) }

func (c *remoteClient) request(ctx context.Context, method, endpoint string, body, output any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("executor: encode runtime request: %w", err)
		}
	}
	retryCtx, cancel := context.WithTimeout(ctx, c.retryTimeout)
	defer cancel()
	delay := 100 * time.Millisecond
	for {
		err := c.requestOnce(retryCtx, method, endpoint, data, output)
		if err == nil {
			return nil
		}
		var status *runtimeHTTPError
		if errors.As(err, &status) && (status.status < 500 || status.status > 599) {
			return err
		}
		if retryCtx.Err() != nil {
			return fmt.Errorf("executor: runtime request: %w", retryCtx.Err())
		}
		// Only transport failures and server errors are retryable. Malformed
		// credentials/responses are permanent, so never loop over those.
		if !errors.Is(err, errRuntimeTransport) && !errors.As(err, &status) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return fmt.Errorf("executor: runtime request: %w", retryCtx.Err())
		case <-timer.C:
		}
		if delay < 2*time.Second {
			delay *= 2
		}
	}
}

var errRuntimeTransport = errors.New("executor: control-plane transport failed")

func (c *remoteClient) requestOnce(ctx context.Context, method, endpoint string, data []byte, output any) error {
	file, err := os.Open(c.tokenFile)
	if err != nil {
		return errors.New("executor: read workload token failed")
	}
	tokenBytes, readErr := io.ReadAll(io.LimitReader(file, 16385))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(tokenBytes) > 16384 {
		return errors.New("executor: invalid workload token file")
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("executor: invalid workload token")
	}
	u := *c.base
	u.Path += endpoint
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
	if err != nil {
		return errors.New("executor: create runtime request failed")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return errRuntimeTransport
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &runtimeHTTPError{status: response.StatusCode}
	}
	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return errRuntimeTransport
	}
	if len(responseBytes) > 1<<20 {
		return errors.New("executor: runtime response exceeds size limit")
	}
	if output != nil {
		if err := json.Unmarshal(responseBytes, output); err != nil {
			return errors.New("executor: invalid runtime response")
		}
	}
	return nil
}
