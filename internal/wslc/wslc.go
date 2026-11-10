// Package wslc drives the WSL container CLI (wslc.exe) that runs Linux jobs for
// the Windows desktop runner. Each job attempt gets one labelled container; the
// CLI's own process is the job's stdio channel and its exit status.
package wslc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/proc"
)

// OwnerLabel marks containers this runner created. It is checked before any
// container is removed, so unrelated containers are never touched.
const (
	OwnerLabel   = "forgelet.owner"
	Owner        = "forgelet-runner"
	JobLabel     = "forgelet.job"
	AttemptLabel = "forgelet.attempt"

	// RunnerBinary is where the Linux executor lives in the runner image.
	RunnerBinary = "/usr/local/bin/forgelet-runner"
	// Workspace is the container's job workspace, on the container filesystem.
	Workspace = "/workspace"
)

// Config is the connection profile's wslc object. The image is administrator
// supplied and should be pinned by digest for reproducible jobs.
type Config struct {
	Image  string   `json:"image"`
	CPUs   float64  `json:"cpus,omitempty"`
	Memory string   `json:"memory,omitempty"`
	DNS    []string `json:"dns,omitempty"`
	CAFile string   `json:"ca_file,omitempty"`
	User   string   `json:"user,omitempty"`
	// ControlURL is the control-plane address the container uses, when the
	// supervisor's URL is only resolvable on Windows (a loopback port forward,
	// say). Empty means the supervisor's URL.
	ControlURL string `json:"control_url,omitempty"`
}

// Validate rejects configurations that cannot start a job. It deliberately does
// not require a digest: a tag is allowed for local trials, while the docs ask
// for a pinned digest wherever reproducibility matters.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Image) == "" || strings.ContainsAny(c.Image, " \t\r\n") {
		return errors.New("wslc image is required")
	}
	if c.CPUs < 0 {
		return errors.New("wslc cpus must not be negative")
	}
	if strings.ContainsAny(c.Memory, " \t\r\n") {
		return errors.New("wslc memory must be a size such as 4G")
	}
	if strings.ContainsAny(c.User, " \t\r\n") {
		return errors.New("wslc user must not contain spaces")
	}
	for _, server := range c.DNS {
		if strings.TrimSpace(server) == "" || strings.ContainsAny(server, " \t\r\n") {
			return errors.New("wslc dns entries must be addresses")
		}
	}
	if c.CAFile != "" && !absolutePath(c.CAFile) {
		return errors.New("wslc ca_file must be an absolute path")
	}
	if c.ControlURL != "" {
		u, err := url.Parse(c.ControlURL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("wslc control_url must be an http(s) URL without credentials or query")
		}
	}
	return nil
}

// absolutePath reports whether p names an absolute location. WSLC settings are
// authored on Windows, but the validation also runs on other hosts (tests and
// the control plane), where filepath.IsAbs only understands the local form. A
// drive-letter path or a UNC share is absolute for the runner wherever the
// check runs, and a POSIX path is absolute inside the WSL distribution.
func absolutePath(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		drive := p[0]
		return drive >= 'A' && drive <= 'Z' || drive >= 'a' && drive <= 'z'
	}
	return strings.HasPrefix(p, `\\`)
}

// Find locates wslc.exe: the installed WSL directory first, then PATH.
func Find() (string, error) {
	candidates := []string{}
	if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
		candidates = append(candidates, filepath.Join(programFiles, "WSL", "wslc.exe"))
	}
	candidates = append(candidates, filepath.Join(os.Getenv("SystemRoot"), "System32", "wslc.exe"))
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	for _, name := range []string{"wslc.exe", "wslc"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("找不到 wslc.exe，请安装或更新 WSL（要求 2.9.3 或更高版本）")
}

// Client runs the CLI at Path.
type Client struct {
	Path string
}

// Command prepares a CLI invocation bound to ctx.
func (c Client) Command(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, c.Path, args...)
	command.Env = os.Environ()
	// The desktop app is a windowed process; without this every CLI call would
	// flash a terminal window.
	proc.Hide(command)
	return command
}

// Output runs a short CLI query and returns its stdout.
func (c Client) Output(ctx context.Context, args ...string) ([]byte, error) {
	return c.Command(ctx, args...).Output()
}

// Version reads the CLI version, which is also the WSL version it ships with.
func (c Client) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := c.Output(ctx, "--version")
	if err != nil {
		// Older builds answer version; the flag above needs no session.
		if output, err = c.Output(ctx, "version"); err != nil {
			return "", fmt.Errorf("wslc version: %w", err)
		}
	}
	line := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	return line, nil
}

// Name builds the container name for one attempt. Names are lowercase because
// the CLI rejects containers whose names are not.
func Name(runID, jobID, attempt string) string {
	return "forgelet-" + short(runID) + "-" + short(jobID) + "-" + short(attempt)
}

func short(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			out.WriteRune(r)
		}
		if out.Len() == 8 {
			break
		}
	}
	if out.Len() == 0 {
		return "attempt"
	}
	return out.String()
}

// RunArgs builds the foreground run invocation. The job's control-plane URL,
// attempt token and workspace travel on stdin, never in argv or container
// metadata.
func RunArgs(config Config, name, jobID, attempt string) []string {
	args := []string{
		"run", "--interactive",
		"--name", name,
		"--label", OwnerLabel + "=" + Owner,
		"--label", JobLabel + "=" + short(jobID),
		"--label", AttemptLabel + "=" + short(attempt),
		"--pull", "missing",
	}
	if config.CPUs > 0 {
		args = append(args, "--cpus", strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", config.CPUs), "0"), "."))
	}
	if config.Memory != "" {
		args = append(args, "--memory", config.Memory)
	}
	for _, server := range config.DNS {
		args = append(args, "--dns", server)
	}
	if config.User != "" {
		args = append(args, "--user", config.User)
	}
	args = append(args, config.Image, RunnerBinary, "--exec")
	return args
}

// ListArgs lists every container this runner owns, running or not.
func ListArgs() []string {
	return []string{"list", "--all", "--format", "json", "--filter", "label=" + OwnerLabel + "=" + Owner}
}

// ParseList returns the owned container names in a list --format json result.
// ParseList returns the container names in a `list --format json` result. The
// CLI prints one object when exactly one container matches and an array
// otherwise, so both shapes are accepted.
func ParseList(data []byte) ([]string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		// The CLI prints nothing when no container matches the filter.
		return nil, nil
	}
	type entry struct {
		Names string `json:"Names"`
	}
	var entries []entry
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, fmt.Errorf("解析 wslc 容器列表: %w", err)
		}
	} else {
		var single entry
		if err := json.Unmarshal(trimmed, &single); err != nil {
			return nil, fmt.Errorf("解析 wslc 容器列表: %w", err)
		}
		entries = []entry{single}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if name := strings.TrimSpace(e.Names); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// InspectArgs reads one container's state.
func InspectArgs(name string) []string {
	return []string{"inspect", "--format", "json", name}
}

// ParseExitCode reads the container's exit status from inspect JSON.
func ParseExitCode(data []byte) (int, error) {
	var entries []struct {
		State struct {
			ExitCode int  `json:"ExitCode"`
			Running  bool `json:"Running"`
		} `json:"State"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return 0, fmt.Errorf("解析 wslc 容器状态: %w", err)
	}
	if len(entries) == 0 {
		return 0, errors.New("wslc 未返回容器状态")
	}
	if entries[0].State.Running {
		return 0, errors.New("wslc 容器仍在运行")
	}
	return entries[0].State.ExitCode, nil
}

// StopArgs stops a container, waiting seconds before the CLI forces it.
func StopArgs(name string, seconds int) []string {
	return []string{"stop", "--time", fmt.Sprint(seconds), name}
}

// RemoveArgs force-removes a container and its anonymous volumes.
func RemoveArgs(name string) []string {
	return []string{"remove", "--force", "--volumes", name}
}
