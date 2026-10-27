package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

// RunActEngine roots the daemon's cgroup view at its Kubernetes resource group.
// Docker's absolute default and Buildx parents must remain below that budget.
func RunActEngine(ctx context.Context, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Unshare(unix.CLONE_NEWCGROUP | unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("isolate act engine cgroup namespace: %w", err)
	}
	if err := unix.Mount("none", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("isolate act engine mount propagation: %w", err)
	}
	if err := unix.Unmount("/sys/fs/cgroup", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach inherited act engine cgroup view: %w", err)
	}
	if err := unix.Mount("none", "/sys/fs/cgroup", "cgroup2", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount bounded act engine cgroup v2: %w", err)
	}
	if err := prepareEngineCgroup("/sys/fs/cgroup"); err != nil {
		return err
	}
	if err := os.Chmod("/tmp", 0777|os.ModeSticky); err != nil {
		return fmt.Errorf("prepare shared act temporary directory: %w", err)
	}
	path, err := exec.LookPath("dockerd")
	if err != nil {
		return fmt.Errorf("find act engine daemon: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.Exec(path, append([]string{"dockerd"}, args...), os.Environ())
}
