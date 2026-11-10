//go:build windows

// Package wsl drives Windows Subsystem for Linux distributions through
// wsl.exe. A job runs natively in the distribution, so the machine's Docker and
// other Linux tooling are available exactly as in a Linux shell.
package wsl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"syscall"
)

// RunnerPath is where the Linux runner lives inside the distribution,
// relative to the account's home directory.
const RunnerPath = ".local/share/forgelet/forgelet-runner"

// Client runs the CLI at Path.
type Client struct {
	Path string
}

// Find locates wsl.exe.
func Find() (string, error) {
	candidates := []string{filepath.Join(os.Getenv("SystemRoot"), "System32", "wsl.exe")}
	if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
		candidates = append(candidates, filepath.Join(programFiles, "WSL", "wsl.exe"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath("wsl.exe"); err == nil {
		return path, nil
	}
	return "", errors.New("找不到 wsl.exe，请启用 WSL2")
}

// Command prepares a CLI invocation bound to ctx.
func (c Client) Command(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, c.Path, args...)
	command.Env = os.Environ()
	// The desktop app is windowed; a child console program must not flash one.
	// wsl.exe only writes list output when it owns a (hidden) console, so
	// CREATE_NO_WINDOW is used here instead of DETACHED_PROCESS.
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	return command
}

// Distros lists installed distributions. wsl.exe prints this one query in
// UTF-16LE, so the output is decoded before parsing.
func (c Client) Distros(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := c.Command(ctx, "--list", "--quiet").Output()
	if err != nil {
		return nil, fmt.Errorf("列出 WSL 发行版: %w", err)
	}
	text := decodeUTF16(output)
	names := []string{}
	for line := range strings.SplitSeq(text, "\n") {
		name := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// decodeUTF16 handles the UTF-16LE output of list queries and the UTF-8 output
// of commands, whichever the CLI used.
func decodeUTF16(data []byte) string {
	if len(data) < 2 {
		return strings.ToValidUTF8(string(data), "")
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) {
		data = data[2:]
	} else if bytes.IndexByte(data, 0) < 0 {
		return string(data) // Already UTF-8.
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
	}
	return string(utf16.Decode(units))
}

// Home returns the distribution's home directory for the configured user.
func (c Client) Home(ctx context.Context, distro, user string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	command := c.Command(ctx, append(c.distroArgs(distro, user), "--exec", "sh", "-c", `printf '%s' "$HOME"`)...)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("启动 WSL 发行版 %q: %w", display(distro), err)
	}
	home := strings.TrimSpace(string(output))
	if !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("WSL 发行版 %q 未返回有效的 HOME", display(distro))
	}
	return home, nil
}

// InstallRunner copies binary into the distribution atomically and returns its
// absolute path there. A file of the same size is left alone.
func (c Client) InstallRunner(ctx context.Context, distro, user, home, binary string, size int64) (string, error) {
	target := home + "/" + RunnerPath
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if current, err := c.size(ctx, distro, user, target); err == nil && current == size {
		return target, nil
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		return "", fmt.Errorf("读取内置执行器: %w", err)
	}
	script := `set -e; mkdir -p "$1"; cat > "$2.tmp"; chmod 0755 "$2.tmp"; mv "$2.tmp" "$2"`
	command := c.Command(ctx, append(c.distroArgs(distro, user), "--exec", "sh", "-c", script, "sh",
		home+"/.local/share/forgelet", target)...)
	command.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("安装 Linux 执行器到 %q: %w %s", display(distro), err, firstLine(stderr.String()))
	}
	return target, nil
}

// size reports a file's size inside the distribution.
func (c Client) size(ctx context.Context, distro, user, path string) (int64, error) {
	command := c.Command(ctx, append(c.distroArgs(distro, user), "--exec", "stat", "-c", "%s", path)...)
	output, err := command.Output()
	if err != nil {
		return -1, err
	}
	var size int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &size); err != nil {
		return -1, err
	}
	return size, nil
}

// DockerVersion reports the distribution's Docker version, or empty when Docker
// is not installed. Job steps and image builds use it directly.
func (c Client) DockerVersion(ctx context.Context, distro, user string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := c.Command(ctx, append(c.distroArgs(distro, user), "--exec", "sh", "-c", `docker version --format '{{.Client.Version}}'`)...)
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// DockerHost reports the daemon endpoint of the distribution's current Docker
// context. The job gets its own HOME, so the supervisor passes this on instead.
func (c Client) DockerHost(ctx context.Context, distro, user string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := c.Command(ctx, append(c.distroArgs(distro, user), "--exec", "sh", "-c",
		`docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null`)...)
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// RunArgs builds the invocation for one job attempt. The Linux runner reads its
// configuration from stdin, so nothing private is on the command line.
func (c Client) RunArgs(distro, user, runner string) []string {
	return append(c.distroArgs(distro, user), "--exec", runner, "--exec")
}

func (c Client) distroArgs(distro, user string) []string {
	args := []string{}
	if distro != "" {
		args = append(args, "--distribution", distro)
	}
	if user != "" {
		args = append(args, "--user", user)
	}
	return args
}

func display(distro string) string {
	if distro == "" {
		return "默认发行版"
	}
	return distro
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if len(line) > 200 {
		line = line[:200]
	}
	return line
}
