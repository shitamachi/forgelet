package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

// actProtocol keeps upstream logging and process lifetime behind the existing
// streaming mask and ACK callback. It never writes the raw JSON to history.
var errActCleanup = errors.New("act workload stop could not be confirmed")

type actProtocol struct {
	ctx     context.Context
	opts    Options
	mask    *masker
	cancel  context.CancelFunc
	line    []byte
	sink    *logSink
	step    string
	result  *run.Result
	failure error
	codes   map[string]int
}

func (p *actProtocol) Write(data []byte) (written int, returnErr error) {
	defer func() {
		if returnErr != nil {
			p.failure = returnErr
			if p.cancel != nil {
				p.cancel()
			}
		}
	}()
	n := len(data)
	p.line = append(p.line, data...)
	for {
		i := bytes.IndexByte(p.line, '\n')
		if i < 0 {
			break
		}
		if i > 1<<20 {
			return 0, fmt.Errorf("act log message exceeds limit")
		}
		var message run.ActMessage
		if err := json.Unmarshal(p.line[:i], &message); err != nil {
			return 0, fmt.Errorf("invalid act worker protocol")
		}
		p.line = p.line[i+1:]
		if p.result != nil {
			return 0, fmt.Errorf("act worker wrote after result")
		}
		if message.Mask != "" {
			if err := p.mask.add(message.Mask); err != nil {
				return 0, err
			}
			continue
		}
		if message.ExitCode != nil && message.StepID != "" {
			if p.codes == nil {
				p.codes = map[string]int{}
			}
			p.codes[message.StepID] = *message.ExitCode
		}
		if message.Result != nil {
			for i := range message.Result.Steps {
				step := &message.Result.Steps[i]
				if step.Outcome == run.Failed {
					if code, ok := p.codes[step.ID]; ok {
						step.ExitCode = code
					} else if message.Result.Error == "" {
						message.Result.Error = "act step failed before reporting a command exit"
					}
				}
			}
			p.result = message.Result
			continue
		}
		if message.StepID != p.step || p.sink == nil {
			if p.sink != nil {
				if err := p.sink.finish(); err != nil {
					return 0, err
				}
			}
			p.step = message.StepID
			emit := p.opts.Log
			if p.opts.ContextLog != nil {
				emit = func(id string, b []byte) error { return p.opts.ContextLog(p.ctx, id, b) }
			}
			p.sink = &logSink{stepID: message.StepID, mask: p.mask, emit: emit, cancel: p.cancel}
		}
		if _, err := p.sink.Write([]byte(message.Message)); err != nil {
			return 0, err
		}
	}
	if len(p.line) > 1<<20 {
		return 0, fmt.Errorf("act log message exceeds limit")
	}
	return n, nil
}
func (p *actProtocol) close() error {
	if p.failure != nil {
		return p.failure
	}
	if len(p.line) != 0 && p.ctx.Err() == nil {
		return fmt.Errorf("incomplete act worker protocol")
	}
	if p.sink != nil {
		return p.sink.finish()
	}
	return nil
}

func executeAct(ctx context.Context, plan run.Plan, opts Options) (result run.Result, returnErr error) {
	result.State = run.Failed
	if opts.ActImage == "" || opts.Workspace == "" {
		return result, fmt.Errorf("act requires an administrator-selected image and workspace")
	}
	if os.Getenv("DOCKER_HOST") != "unix://"+run.ActSocket {
		return result, fmt.Errorf("act requires its Pod-owned Docker socket")
	}
	if err := run.ValidateImageMirrors(opts.ActImage, opts.ImageMirrors); err != nil {
		return result, err
	}
	if err := prepareProcessReaping(); err != nil {
		return result, err
	}
	if err := os.MkdirAll(opts.Workspace, 0700); err != nil {
		return result, err
	}
	workspace, err := filepath.Abs(opts.Workspace)
	if err != nil {
		return result, err
	}
	seconds := plan.TimeoutSeconds
	if seconds <= 0 {
		seconds = 1200
	}
	jobCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	mask := newMasker()
	for _, value := range append(append([]string(nil), opts.Masks...), opts.CloneToken) {
		if err := mask.add(value); err != nil {
			return result, err
		}
	}
	if opts.CloneToken != "" {
		if err := mask.add(base64.StdEncoding.EncodeToString([]byte("x-access-token:" + opts.CloneToken))); err != nil {
			return result, err
		}
	}
	for _, value := range opts.Secrets {
		if err := mask.add(value); err != nil {
			return result, err
		}
	}
	if err := maskRegistry(opts.RegistryConfig, mask); err != nil {
		return result, err
	}
	defer func() {
		if returnErr != nil {
			result.State = run.Failed
			result.Error = publicError(returnErr, mask)
		}
		for name, value := range result.Outputs {
			if strings.Contains(value, "${{") || containsSecret(value, mask) {
				delete(result.Outputs, name)
			}
		}
		for i := range result.Steps {
			for name, value := range result.Steps[i].Outputs {
				if containsSecret(value, mask) {
					delete(result.Steps[i].Outputs, name)
				}
			}
		}
		for _, value := range mask.values {
			result.Error = strings.ReplaceAll(result.Error, value, "***")
		}
	}()
	// All containers belong to this one engine/job. Stop them before reporting
	// cancellation or timeout, including Testcontainers and nested build helpers.
	defer func() {
		cleanupCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer done()
		if err := stopActContainers(cleanupCtx); err != nil {
			returnErr = errors.Join(returnErr, errActCleanup, err)
		}
	}()
	if err := mask.add(opts.PackageToken); err != nil {
		return result, err
	}
	cacheHost := opts.ActCacheHost
	if cacheHost == "" {
		cacheHost = "127.0.0.1"
	}
	cache, err := startActCacheAt(jobCtx, opts.Services, cacheHost, opts.ActCacheObjectBytes)
	if err != nil {
		return result, err
	}
	defer cache.Close()
	if err := mask.add(cache.capability); err != nil {
		return result, err
	}
	if err := mask.add(cache.token); err != nil {
		return result, err
	}
	protocol := &actProtocol{ctx: jobCtx, opts: opts, mask: mask, cancel: cancel}
	defer func() {
		if err := protocol.close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	// Pull with the supervisor's read-only registry config; the job container
	// receives neither that config nor the control-plane workload token.
	pull := exec.CommandContext(jobCtx, "docker", "pull", opts.ActImage)
	pull.Env = append(os.Environ(), "DOCKER_CONFIG="+opts.RegistryConfig)
	pullSink := &logSink{mask: mask, stepID: "", emit: func(id string, b []byte) error {
		if opts.ContextLog != nil {
			return opts.ContextLog(jobCtx, id, b)
		}
		if opts.Log != nil {
			return opts.Log(id, b)
		}
		return nil
	}, cancel: cancel}
	pull.Stdout, pull.Stderr = pullSink, pullSink
	image := opts.ActImage
	code := 0
	if local, verified := preloadedActImage(jobCtx, opts); verified {
		image = local
		_, err = pullSink.Write([]byte("Using verified preloaded act image " + local + "\n"))
	} else {
		code, err = runProcess(jobCtx, pull)
	}
	if flushErr := pullSink.finish(); flushErr != nil {
		return result, flushErr
	}
	if err != nil || code != 0 {
		if jobCtx.Err() != nil {
			result.State = contextState(jobCtx)
			return result, nil
		}
		return result, fmt.Errorf("prepare act runtime image")
	}
	if err := prepareActMirrors(jobCtx, opts, pullSink); err != nil {
		if jobCtx.Err() != nil {
			result.State = contextState(jobCtx)
			return result, nil
		}
		return result, err
	}
	request := run.ActRequest{CPUCount: opts.ActCPUCount, BuildkitImage: opts.ImageMirrors["moby/buildkit:buildx-stable-1"], BuildkitConfig: opts.BuildkitConfig, PackageToken: opts.PackageToken, Plan: plan, Workspace: workspace, Image: image, Secrets: opts.Secrets, Token: opts.CloneToken, CacheURL: cache.URL(), CacheToken: cache.token, ActionBaseURL: opts.ActionBaseURL}
	data, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	worker := opts.ActWorker
	if worker == "" {
		worker = "/usr/local/bin/forgelet-act"
	}
	cmd := exec.CommandContext(jobCtx, worker)
	cmd.Env = append(os.Environ(), "ACTIONS_RUNTIME_TOKEN="+cache.token, "ACTIONS_RUNTIME_URL="+cache.URL())
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = protocol
	code, err = runProcess(jobCtx, cmd)
	if protocol.failure != nil {
		return result, protocol.failure
	}
	if jobCtx.Err() != nil {
		result.State = contextState(jobCtx)
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("act protocol failed: %w", err)
	}
	if protocol.result == nil {
		return result, fmt.Errorf("act worker exited without a result (status %d)", code)
	}
	result = *protocol.result
	if !result.State.Terminal() {
		return result, fmt.Errorf("act worker returned a nonterminal result")
	}
	if code != 0 && result.Error == "" {
		return result, fmt.Errorf("act worker process failed")
	}
	return result, nil
}
func containsSecret(value string, mask *masker) bool {
	for _, secret := range mask.values {
		if strings.Contains(value, secret) {
			return true
		}
	}
	return false
}
func stopActContainers(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "ps", "-q")
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("list owned act containers: %w", err)
	}
	ids := strings.Fields(string(data))
	if len(ids) == 0 {
		return nil
	}
	// Prove execution has stopped without waiting for overlay filesystem removal.
	// The entire private engine and its data are reclaimed with the Pod. Removing
	// a large build container here can exhaust the result-reporting grace period.
	args := append([]string{"kill"}, ids...)
	killErr := exec.CommandContext(ctx, "docker", args...).Run()
	remaining, err := exec.CommandContext(ctx, "docker", "ps", "-q").Output()
	if err != nil {
		return fmt.Errorf("confirm owned act containers stopped: %w", err)
	}
	if len(strings.Fields(string(remaining))) != 0 {
		if killErr != nil {
			return fmt.Errorf("stop owned act containers: %w", killErr)
		}
		return fmt.Errorf("owned act containers are still running")
	}
	// A container can exit between listing and kill; the final observation is
	// authoritative even when Docker returns an already-stopped error.
	return nil
}
