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
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/executor"
	"github.com/shitamachi/forgelet/internal/run"
)

type Config struct {
	URL           string `json:"url"`
	LegacyProfile string `json:"profile,omitempty"` // Old servers still require this field.
	Token         string `json:"token"`
}

type Status struct {
	State   string             `json:"state,omitempty"`
	Log     *run.Log           `json:"log,omitempty"`
	Message string             `json:"message,omitempty"`
	Job     *run.ExternalGrant `json:"job,omitempty"`
}

type client struct {
	url    *url.URL
	http   http.Client
	config Config
}

type request struct {
	Profile string `json:"profile,omitempty"`
	Attempt string `json:"attempt"`
	JobID   string `json:"job_id,omitempty"`
}

func Run(ctx context.Context, cfg Config, workRoot string, status func(Status)) error {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1"))) {
		return errors.New("控制面地址必须使用 HTTPS，本机回环地址可用 HTTP")
	}
	token, ok := strings.CutPrefix(cfg.Token, "frr_")
	if _, err := hex.DecodeString(token); !ok || err != nil || len(token) != 64 {
		return errors.New("请输入有效的设备凭据")
	}
	c := client{url: u, config: cfg, http: http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	var attempt string
	var last Status
	reconnecting := false
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		if attempt == "" {
			var nonce [32]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return fmt.Errorf("create attempt token: %w", err)
			}
			attempt = "frja_" + hex.EncodeToString(nonce[:])
		}
		var grant run.ExternalGrant
		sent := time.Now()
		code, err := c.send(ctx, "/poll", request{Profile: cfg.LegacyProfile, Attempt: attempt}, &grant)
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
			return errors.New("控制面拒绝了设备凭据")
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
	result := Status{State: "failed", Job: &grant}
	dir, err := os.MkdirTemp(workRoot, "attempt-")
	if err != nil {
		result.Message = "无法创建临时工作区"
		return result
	}
	defer func() { _ = os.RemoveAll(dir) }()
	execution, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		c.renew(execution, request{Profile: c.config.LegacyProfile, JobID: grant.JobID, Attempt: token}, deadline)
	}()
	var reported run.State
	err = executor.RunRemote(execution, executor.RemoteOptions{URL: c.url.String(), JobID: grant.JobID, AttemptToken: token,
		Workspace: dir, Client: &c.http, RetryTimeout: 25 * time.Second, FlushTimeout: 8 * time.Second,
		OnResult: func(state run.State) { reported = state },
		OnLog:    func(entry run.Log) { status(Status{Log: &entry}) }})
	stopped := execution.Err() != nil
	cancel()
	<-done
	switch {
	case err == nil:
		result.State = "succeeded"
	case reported == run.Cancelled || stopped:
		result.State = "stopped"
		result.Message = "任务停止，最终状态请查看控制面"
	case reported == run.TimedOut:
		result.State = "timed_out"
		result.Message = "任务超时，详细日志请查看控制面"
	default:
		result.Message = "任务未成功，详细日志请查看控制面"
	}
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

func buildPlatforms(ctx context.Context) string {
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probe, "docker", "buildx", "inspect", "--timeout", "2s").Output()
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
