// Package actkernel builds isolated, pinned execution-kernel probes. It is a
// development harness, not an executor backend or part of the control plane.
package actkernel

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Both upstreams declare github.com/nektos/act as their module path. Building in
// separate temporary modules avoids replacements in forgelet's production graph.
//
//go:embed worker.go.txt
var workerSource string

//go:embed worker_test.go.txt
var workerTests string

//go:embed locks/*
var locks embed.FS

//go:embed actions.json
var actionPins []byte

// PrepareActionSources downloads the same immutable public Action snapshots for
// both candidates. No GitHub credential is sent to this public archive endpoint.
func PrepareActionSources(ctx context.Context, dir string) error {
	var pins map[string]string
	if err := json.Unmarshal(actionPins, &pins); err != nil {
		return fmt.Errorf("read action pins: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	defer client.CloseIdleConnections()
	for key, sha := range pins {
		repo, _, ok := strings.Cut(key, "@")
		if !ok || len(sha) != 40 {
			return fmt.Errorf("invalid action pin %q", key)
		}
		url := "https://codeload.github.com/" + repo + "/tar.gz/" + sha
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("fetch action %s: %w", key, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<20)+1))
		_ = response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read action %s: %w", key, readErr)
		}
		if response.StatusCode != http.StatusOK || len(data) > 64<<20 {
			return fmt.Errorf("fetch action %s: HTTP %d, bytes %d", key, response.StatusCode, len(data))
		}
		if err := os.WriteFile(filepath.Join(dir, sha+".tar.gz"), data, 0o600); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "pins.json"), actionPins, 0o600)
}

type Candidate string

const (
	Upstream Candidate = "upstream"
	Gitea    Candidate = "gitea"
)

func (c Candidate) module() (string, error) {
	switch c {
	case Upstream:
		return "github.com/nektos/act v0.2.89", nil
	case Gitea:
		return "gitea.com/gitea/act v0.262.0", nil
	default:
		return "", fmt.Errorf("unknown execution kernel %q", c)
	}
}

// Prepare writes the identical probe and behavior tests for each candidate;
// compat.go contains only the upstream API signature differences.
func Prepare(dir string, candidate Candidate) error {
	_, err := candidate.module()
	if err != nil {
		return err
	}
	if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && !strings.HasPrefix(string(data), "module forgelet-kernel-probe\n") {
		return fmt.Errorf("refusing to overwrite an unrelated Go module in %s", dir)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect probe directory: %w", err)
	}
	manifest, err := locks.ReadFile("locks/" + string(candidate) + ".mod")
	if err != nil {
		return fmt.Errorf("read module lock: %w", err)
	}
	sums, err := locks.ReadFile("locks/" + string(candidate) + ".sum")
	if err != nil {
		return fmt.Errorf("read checksum lock: %w", err)
	}
	plannerArgs, readerArgs, cacheArgs := "path, false, false", "r, false", `dir, "", listenIP, 0, logger`
	tune := `cfg.ConcurrentJobs = 1`
	if candidate == Gitea {
		plannerArgs, readerArgs, cacheArgs = "path, false", "r", `dir, listenIP, 0, logger`
		tune = `cfg.MaxParallel = 1
 cfg.DefaultActionInstance = "github.com"
 level := logrus.InfoLevel
 cfg.JobLoggerLevel = &level
 if req.Docker { cfg.ValidVolumes = []string{"/cache"} }
 // Gitea's NoSkipCheckout also switches local reusable workflows to remote
 // resolution. The probe already owns a fixed local snapshot.
 if req.Mode == "workflow" { cfg.NoSkipCheckout = false }`
	}
	compat := fmt.Sprintf(`package main
import (
 "io"
 "github.com/nektos/act/pkg/model"
 "github.com/nektos/act/pkg/artifactcache"
 "github.com/nektos/act/pkg/runner"
 "github.com/sirupsen/logrus"
)
func planner(path string) (model.WorkflowPlanner, error) { return model.NewWorkflowPlanner(%s) }
func readWorkflow(r io.Reader) (*model.Workflow, error) { return model.ReadWorkflow(%s) }
func startCache(dir, listenIP string, logger *logrus.Logger) (*artifactcache.Handler, error) { return artifactcache.StartHandler(%s) }
func tune(cfg *runner.Config, req request) { %s }
`, plannerArgs, readerArgs, cacheArgs, tune)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create probe module: %w", err)
	}
	for name, content := range map[string]string{"go.mod": string(manifest), "go.sum": string(sums), "main.go": workerSource, "main_test.go": workerTests, "compat.go": compat} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			return fmt.Errorf("write probe %s: %w", name, err)
		}
	}
	return nil
}

// Build compiles a throwaway worker. Its dependencies never enter the server.
func Build(ctx context.Context, dir, output, goos, goarch string) error {
	for _, args := range [][]string{{"build", "-mod=readonly", "-trimpath", "-o", output, "."}} {
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if args[0] == "build" {
			cmd.Env = append(cmd.Env, "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
		}
		if data, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("probe go %s: %w\n%s", strings.Join(args, " "), err, data)
		}
	}
	return nil
}
