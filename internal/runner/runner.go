// Package runner executes one leased job at a time while its desktop session is open.
package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/proc"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/sysinfo"
	"github.com/shitamachi/forgelet/internal/wslc"
)

type Config struct {
	URL   string       `json:"url"`
	Token string       `json:"token,omitempty"` // Optional; otherwise the desktop pairs and stores one.
	WSLC  *wslc.Config `json:"wslc,omitempty"`  // Windows only: the Linux container backend.
}

type Status struct {
	State     string             `json:"state,omitempty"`
	Log       *run.Log           `json:"log,omitempty"`
	Message   string             `json:"message,omitempty"`
	Job       *run.ExternalGrant `json:"job,omitempty"`
	Code      string             `json:"code,omitempty"`      // Pairing code shown until an administrator approves it.
	ExpiresAt time.Time          `json:"expires_at,omitzero"` // When that code stops working.
	Device    string             `json:"device,omitempty"`    // Name the administrator gave this machine.
}

// ExecRequest is what the Windows runner writes to the Linux container's stdin:
// one job attempt and the private material it needs. It is never in argv.
type ExecRequest struct {
	URL       string `json:"url"`
	JobID     string `json:"job_id"`
	Attempt   string `json:"attempt"`
	Workspace string `json:"workspace"`
	CACert    string `json:"ca_cert,omitempty"`
}

type client struct {
	url     *url.URL
	http    http.Client
	config  Config
	backend backend
}

// backend starts one job attempt in the environment this platform provides.
type backend interface {
	executeLease(ctx context.Context, client *client, workRoot string, grant run.ExternalGrant, token string, status func(Status)) Status
	// describe names the backend and, where one exists, its engine version.
	describe() descriptor
}

type request struct {
	Attempt string          `json:"attempt"`
	JobID   string          `json:"job_id,omitempty"`
	Info    *run.RunnerInfo `json:"info,omitempty"`
}

// infoInterval is how often a device re-reads its own numbers. It is a handful
// of system calls and one small request, so a busy machine pays almost nothing.
const infoInterval = 30 * time.Second

// describe is the backend's name and engine version, shown with the device.
type descriptor struct {
	backend string
	engine  string
}

// collectInfo reads the device's current description. Unknown readings stay
// zero rather than costing extra work.
func collectInfo(desc descriptor) *run.RunnerInfo {
	sample := sysinfo.Read()
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	return &run.RunnerInfo{
		OS: runtime.GOOS, Arch: runtime.GOARCH, Version: version(), Hostname: hostname,
		Backend: desc.backend, Engine: desc.engine,
		CPUs: sample.CPUs, MemoryTotalBytes: sample.MemoryTotal,
		MemoryAvailableBytes: sample.MemoryAvailable, Load1: sample.Load1,
		ReportedAt: time.Now().UTC(),
	}
}

// version is the runner's own version, empty for development builds.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	return ""
}

func Run(ctx context.Context, cfg Config, workRoot string, status func(Status)) error {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1"))) {
		return errors.New("控制面地址必须使用 HTTPS，本机回环地址可用 HTTP")
	}
	token := cfg.Token
	if token == "" {
		token = readCredential(workRoot)
	}
	if token == "" {
		paired, err := pair(ctx, cfg.URL, workRoot, status)
		if err != nil {
			return err
		}
		token = paired
	}
	if !validToken(token) {
		return errors.New("设备凭据无效，请重新配对")
	}
	cfg.Token = token
	execution, err := newBackend(ctx, cfg, status)
	if err != nil {
		return err
	}
	c := client{url: u, config: cfg, backend: execution, http: http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	var attempt string
	var last Status
	reconnecting := false
	backoff := 2 * time.Second
	var info *run.RunnerInfo
	var nextInfo time.Time
	for ctx.Err() == nil {
		if attempt == "" {
			var nonce [32]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return fmt.Errorf("create attempt token: %w", err)
			}
			attempt = "frja_" + hex.EncodeToString(nonce[:])
		}
		if !time.Now().Before(nextInfo) {
			info = collectInfo(execution.describe())
			nextInfo = time.Now().Add(infoInterval)
		}
		var grant run.ExternalGrant
		sent := time.Now()
		code, err := c.send(ctx, "/poll", request{Attempt: attempt, Info: info}, &grant)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if err != nil || code >= 500 || code == http.StatusTooManyRequests {
			reconnecting = true
			status(Status{State: "reconnecting", Message: "暂时无法连接控制面，正在重试", Job: last.Job})
			wait(ctx, backoff)
			backoff = min(15*time.Second, backoff*2)
			continue
		}
		backoff = 2 * time.Second
		switch code {
		case http.StatusOK:
			deadline := sent.Add(time.Duration(grant.Seconds) * time.Second)
			if grant.JobID == "" || grant.Seconds > int(run.ExternalLease.Seconds()) || !time.Now().Before(deadline) {
				return errors.New("控制面返回的执行租约无效或已过期")
			}
			status(Status{State: "running", Job: &grant})
			last = c.execute(ctx, workRoot, grant, attempt, deadline, status)
			status(last)
		case http.StatusNoContent, http.StatusConflict:
			if last.Job == nil {
				status(Status{State: "available"})
			} else if reconnecting {
				status(last)
			}
		case http.StatusUnauthorized:
			// The credential was rotated or the device removed: pair again and
			// show a fresh code rather than failing quietly.
			dropCredential(workRoot)
			paired, err := pair(ctx, cfg.URL, workRoot, status)
			if err != nil {
				return err
			}
			c.config.Token = paired
			attempt = ""
			continue
		case http.StatusForbidden:
			return errors.New("当前设备无法访问配置的工作流仓库")
		default:
			return fmt.Errorf("控制面返回 HTTP %d", code)
		}
		reconnecting = false
		attempt = ""
		wait(ctx, 3*time.Second+mathrand.N(time.Second))
	}
	return nil
}

func (c *client) execute(ctx context.Context, workRoot string, grant run.ExternalGrant, token string, deadline time.Time, status func(Status)) Status {
	execution, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// A cancellation or a lost lease ends the local attempt, so the
		// backend must stop its container or process group.
		defer cancel()
		c.renew(execution, request{JobID: grant.JobID, Attempt: token}, deadline)
	}()
	result := c.backend.executeLease(execution, c, workRoot, grant, token, status)
	cancel()
	<-done
	return result
}

func (c *client) renew(ctx context.Context, input request, deadline time.Time) {
	for ctx.Err() == nil {
		left := time.Until(deadline)
		if left <= 0 {
			return
		}
		wait(ctx, min(8*time.Second, left))
		sent := time.Now()
		if ctx.Err() != nil || !sent.Before(deadline) {
			return
		}
		var reply struct {
			Cancel   bool `json:"cancel"`
			Terminal bool `json:"terminal"`
			Seconds  int  `json:"lease_seconds"`
		}
		poll, cancel := context.WithDeadline(ctx, deadline)
		code, err := c.send(poll, "/renew", input, &reply)
		cancel()
		if err != nil || code >= 500 || code == http.StatusTooManyRequests {
			continue
		}
		if code != http.StatusOK || reply.Cancel || reply.Terminal || reply.Seconds <= 0 || reply.Seconds > int(run.ExternalLease.Seconds()) {
			return
		}
		deadline = sent.Add(time.Duration(reply.Seconds) * time.Second)
	}
}

func (c *client) send(ctx context.Context, endpoint string, input request, output any) (int, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return 0, err
	}
	u := *c.url
	u.Path = strings.TrimRight(u.Path, "/") + "/runner/v1" + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.config.Token)
	req.Header.Set("Content-Type", "application/json")
	if endpoint == "/poll" {
		req.Header.Set(run.RunnerBuildPlatformsHeader, buildPlatforms(ctx))
		if labels := deviceLabels(c.config); len(labels) > 0 {
			req.Header.Set(run.RunnerLabelsHeader, strings.Join(labels, ","))
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusOK {
		err = json.NewDecoder(io.LimitReader(res.Body, 16<<10)).Decode(output)
	}
	return res.StatusCode, err
}

// DesktopState names an executor state the way the desktop app does.
func DesktopState(state run.State) string {
	switch state {
	case run.Succeeded:
		return "succeeded"
	case run.Failed:
		return "failed"
	case run.Cancelled, run.Interrupted:
		return "stopped"
	case run.TimedOut:
		return "timed_out"
	}
	return string(state)
}

func buildPlatforms(ctx context.Context) string {
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(probe, "docker", "buildx", "inspect", "--timeout", "2s")
	proc.Hide(command)
	output, err := command.Output()
	if err != nil {
		return "none"
	}
	return runningBuildPlatforms(string(output))
}

func runningBuildPlatforms(output string) string {
	supported := map[string]bool{}
	running := false
	for line := range strings.SplitSeq(output, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), ":")
		switch key {
		case "Status":
			running = strings.TrimSpace(value) == "running"
		case "Platforms":
			if running {
				for item := range strings.SplitSeq(value, ",") {
					platform := strings.TrimSuffix(strings.TrimSpace(item), "*")
					if run.ValidBuildPlatform(platform) {
						supported[platform] = true
					}
				}
			}
		}
	}
	var platforms []string
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		if supported[platform] {
			platforms = append(platforms, platform)
		}
	}
	if len(platforms) == 0 {
		return "none"
	}
	return strings.Join(platforms, ",")
}

func wait(ctx context.Context, interval time.Duration) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
