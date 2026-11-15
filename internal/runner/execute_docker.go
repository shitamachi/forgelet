package runner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

type dockerCommand func(context.Context, ...string) *exec.Cmd

const dockerWorker = "/usr/local/bin/forgelet-runner"
const dockerOwnerLabel = "forgelet.dev/runner-owner"

type dockerExecutor struct {
	binaryDir string
	execution backend
	command   dockerCommand
	owner     string
	approved  []run.RunnerContainer
	checked   map[run.RunnerContainer]time.Time
	ready     []run.RunnerContainer
	cleaned   bool
}

func newDockerExecutor(execution backend, root string) *dockerExecutor {
	driver, ok := execution.(interface{ dockerCommand() dockerCommand })
	if !ok {
		return nil
	}
	command := driver.dockerCommand()
	if command == nil {
		return nil
	}
	host, _ := os.Hostname()
	owner := sha256.Sum256([]byte(host + "\x00" + root))
	executable, _ := os.Executable()
	return &dockerExecutor{binaryDir: filepath.Dir(executable), execution: execution, command: command,
		owner: fmt.Sprintf("%x", owner[:16]), checked: make(map[run.RunnerContainer]time.Time)}
}

func (d *dockerExecutor) prepare(ctx context.Context, status func(Status)) {
	if !d.cleaned && len(d.approved) > 0 {
		probe, cancel := context.WithTimeout(ctx, 6*time.Second)
		d.cleaned = d.removeOrphans(probe)
		cancel()
		if !d.cleaned {
			return
		}
	}
	d.ready = slices.DeleteFunc(d.ready, func(c run.RunnerContainer) bool {
		return !slices.Contains(d.approved, c)
	})
	for c := range d.checked {
		if !slices.Contains(d.approved, c) {
			delete(d.checked, c)
		}
	}
	for _, c := range d.approved {
		if time.Now().Before(d.checked[c]) {
			continue
		}
		if d.check(ctx, c, status) {
			if !slices.Contains(d.ready, c) {
				d.ready = append(d.ready, c)
				status(Status{State: "available", Message: "Linux 执行镜像已就绪 " + c.Image})
			}
		} else {
			d.ready = slices.DeleteFunc(d.ready, func(other run.RunnerContainer) bool { return c == other })
		}
		d.checked[c] = time.Now().Add(30 * time.Second)
		// Only prepare one image between polls.
		break
	}
}

func (d *dockerExecutor) check(ctx context.Context, c run.RunnerContainer, status func(Status)) bool {
	if c.Kernel == "act" {
		return d.checkAct(ctx, c, status)
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	missing := d.command(check, "image", "inspect", c.Image).Run() != nil
	cancel()
	if missing {
		status(Status{State: "preparing", Message: "正在拉取 Linux 执行镜像 " + c.Image})
		pull, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := d.command(pull, "pull", "--platform", c.Platform, c.Image).Run()
		cancel()
		if err != nil {
			status(Status{State: "reconnecting", Message: "无法拉取执行镜像，请检查 Docker 和镜像仓库连接"})
			return false
		}
	}
	arch := "x86_64"
	if c.Platform == "linux/arm64" {
		arch = "aarch64"
	}
	probe, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return d.command(probe, "run", "--rm", "--pull=never", "--platform", c.Platform, "--network=none",
		"--user", "1000:1000", "--entrypoint", "/bin/sh", c.Image, "-ec",
		`test -x "$1" && test "$(uname -m)" = "$2"`, "sh", dockerWorker, arch).Run() == nil
}

func (d *dockerExecutor) execute(ctx context.Context, c *client, grant run.ExternalGrant, token string, status func(Status)) Status {
	if grant.Container == nil || !grant.Container.Valid() || !slices.Contains(d.ready, *grant.Container) {
		return Status{State: "failed", Job: &grant, Message: "任务要求的 Linux 执行环境未就绪"}
	}
	if grant.Container.Kernel == "act" {
		return d.executeAct(ctx, c, grant, token, status)
	}
	name := "forgelet-job-" + run.ExternalAttempt(token)
	environment := *grant.Container
	command := d.command(context.WithoutCancel(ctx), "run", "--interactive", "--rm", "--pull=never",
		"--name", name, "--label", dockerOwnerLabel+"="+d.owner, "--platform", environment.Platform,
		"--user", "1000:1000", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--workdir", "/tmp", "--entrypoint", dockerWorker, environment.Image, "--exec")
	remove := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		defer cancel()
		filter := "label=" + dockerOwnerLabel + "=" + d.owner
		output, err := d.command(cleanup, "ps", "-aq", "--filter", filter, "--filter", "name=^/"+name+"$").Output()
		if id := strings.TrimSpace(string(output)); err == nil && id != "" {
			_ = d.command(cleanup, "rm", "-f", id).Run()
		}
	}
	result := executeChild(ctx, command, ExecRequest{URL: c.url.String(), JobID: grant.JobID, Attempt: token}, grant, status, remove)
	remove()
	return result
}

func (d *dockerExecutor) actBinaries() (string, string, error) {
	// A backend that keeps its binaries inside a distribution reports paths its
	// own Docker CLI can read; host paths do not exist there.
	if source, ok := d.execution.(interface{ containerBinaries() (string, string, bool) }); ok {
		if worker, act, ok := source.containerBinaries(); ok {
			return worker, act, nil
		}
	}
	for _, dir := range []string{d.binaryDir, filepath.Join(d.binaryDir, "..", "Resources")} {
		worker := filepath.Join(dir, "forgelet-runner-linux-"+goruntime.GOARCH)
		act := filepath.Join(dir, "forgelet-act-linux-"+goruntime.GOARCH)
		w, we := os.Stat(worker)
		a, ae := os.Stat(act)
		if we == nil && ae == nil && w.Mode().IsRegular() && a.Mode().IsRegular() {
			return worker, act, nil
		}
	}
	return "", "", fmt.Errorf("桌面应用缺少 Linux 完整执行内核，请更新应用")
}

func (d *dockerExecutor) checkAct(ctx context.Context, c run.RunnerContainer, status func(Status)) bool {
	if _, _, err := d.actBinaries(); err != nil {
		status(Status{State: "preparing", Message: err.Error()})
		return false
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	missing := d.command(probe, "image", "inspect", c.Image).Run() != nil
	cancel()
	if missing {
		status(Status{State: "preparing", Message: "正在准备完整 Linux 执行内核"})
		pull, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := d.command(pull, "pull", "--platform", "linux/"+goruntime.GOARCH, c.Image).Run()
		cancel()
		if err != nil {
			return false
		}
	}
	probe, cancel = context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := d.command(probe, "run", "--rm", "--pull=never", "--platform", "linux/"+goruntime.GOARCH, "--network=none", "--entrypoint", "/bin/sh", c.Image, "-ec", "command -v dockerd >/dev/null && docker --version >/dev/null && df -Pk /var/lib/docker | awk 'NR==2 {print $4}'").Output()
	if err != nil {
		return false
	}
	available, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || available < 0 || available > 1<<50 {
		return false
	}
	if available*1024 < c.DiskBytes {
		status(Status{State: "preparing", Message: fmt.Sprintf("Docker 可用空间不足：需要 %.1f GiB，当前 %.1f GiB", float64(c.DiskBytes)/(1<<30), float64(available)/float64(1<<20))})
		return false
	}
	return true
}

func (d *dockerExecutor) executeAct(ctx context.Context, c *client, grant run.ExternalGrant, token string, status func(Status)) Status {
	result := Status{State: "failed", Job: &grant}
	worker, act, err := d.actBinaries()
	if err != nil {
		result.Message = err.Error()
		return result
	}
	env := *grant.Container
	name := "forgelet-job-" + run.ExternalAttempt(token)
	remove := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		output, err := d.command(cleanup, "ps", "-aq", "--filter", "label="+dockerOwnerLabel+"="+d.owner, "--filter", "name=^/"+name+"$").Output()
		if id := strings.TrimSpace(string(output)); err == nil && id != "" {
			_ = d.command(cleanup, "rm", "-f", "-v", id).Run()
		}
	}
	defer remove()
	// The device credential stays in the desktop. Only this attempt is sent on
	// stdin; the job sees an isolated engine, never the host Docker socket.
	script := `mkdir -p /workspace/repository /run/forgelet-act
export DOCKER_HOST=unix:///run/forgelet-act/docker.sock
export FORGELET_ACT_CACHE_HOST="$(hostname -i | awk '{print $1}')"
/usr/local/bin/forgelet-runner --engine --host=unix:///run/forgelet-act/docker.sock --storage-driver=overlay2 --exec-opt=native.cgroupdriver=cgroupfs >/tmp/forgelet-engine.log 2>&1 </dev/null &
for n in $(seq 1 120); do if docker info >/dev/null 2>&1; then exec /usr/local/bin/forgelet-runner --exec; fi; sleep 1; done
cat /tmp/forgelet-engine.log >&2; exit 1`
	args := []string{"create", "--interactive", "--name", name, "--label", dockerOwnerLabel + "=" + d.owner, "--pull=never", "--platform", "linux/" + goruntime.GOARCH, "--privileged", "--cpus", strconv.FormatInt(env.CPUs, 10), "--cpuset-cpus", "0-" + strconv.FormatInt(env.CPUs-1, 10), "--memory", strconv.FormatInt(env.MemoryBytes, 10), "--entrypoint", "/bin/sh", env.Image, "-ec", script}
	if err = d.command(ctx, args...).Run(); err != nil {
		result.Message = "无法创建完整任务沙箱"
		return result
	}
	for _, file := range []struct{ from, to string }{{worker, dockerWorker}, {act, "/usr/local/bin/forgelet-act"}} {
		if err = d.command(ctx, "cp", file.from, name+":"+file.to).Run(); err != nil {
			result.Message = "无法安装任务执行内核"
			return result
		}
	}
	command := d.command(context.WithoutCancel(ctx), "start", "--attach", "--interactive", name)
	return executeChild(ctx, command, ExecRequest{URL: c.url.String(), JobID: grant.JobID, Attempt: token, Workspace: "/workspace/repository"}, grant, status, remove)
}

func (d *dockerExecutor) removeOrphans(ctx context.Context) bool {
	output, err := d.command(ctx, "ps", "-aq", "--filter", "label="+dockerOwnerLabel+"="+d.owner).Output()
	if err != nil {
		return false
	}
	ids := strings.Fields(string(output))
	return len(ids) == 0 || d.command(ctx, append([]string{"rm", "-f", "-v"}, ids...)...).Run() == nil
}
