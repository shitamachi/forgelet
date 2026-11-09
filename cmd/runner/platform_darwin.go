//go:build darwin

package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/shitamachi/forgelet/internal/runner"
)

// The macOS app relies on the account's system trust store.
func prepareTLS(runner.Config) error { return nil }

func checkPlatform() error {
	if runtime.GOARCH != "arm64" {
		return errors.New("this runner requires an Apple Silicon Mac")
	}
	// Finder launches without a shell environment. Use only known tool directories.
	return os.Setenv("PATH", "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
}

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
