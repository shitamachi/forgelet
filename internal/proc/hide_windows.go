//go:build windows

package proc

import (
	"os/exec"
	"syscall"
)

// DETACHED_PROCESS: the child gets no console at all, so a windowed app never
// flashes a terminal. The caller still gets its pipes.
const detachedProcess = 0x00000008

func hide(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= detachedProcess
}
