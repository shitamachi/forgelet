// Package proc prepares child processes for a GUI application: on Windows a
// console program started from a windowed process opens a console window unless
// the caller asks otherwise, which looks like a terminal flashing on screen.
package proc

import "os/exec"

// Hide keeps a child's console hidden. It has no effect off Windows.
func Hide(command *exec.Cmd) {
	hide(command)
}
