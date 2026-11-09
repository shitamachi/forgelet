//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/shitamachi/forgelet/internal/runner"
)

func checkPlatform() error { return nil }

// Native Linux execution uses the host's system trust store.
func prepareTLS(runner.Config) error { return nil }

func lockRoot(root string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("a Forgelet desktop runner is already active on this account")
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
