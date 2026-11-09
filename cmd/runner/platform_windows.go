//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"github.com/shitamachi/forgelet/internal/runner"
)

func checkPlatform() error { return nil }

// prepareTLS trusts the connection profile's CA for this process as well as the
// job container. Go replaces its system roots when SSL_CERT_FILE is set, so the
// file must contain the control plane's trust chain.
func prepareTLS(cfg runner.Config) error {
	if cfg.WSLC == nil || cfg.WSLC.CAFile == "" {
		return nil
	}
	return os.Setenv("SSL_CERT_FILE", cfg.WSLC.CAFile)
}

// lockRoot serialises desktop runners per account. Windows releases the lock
// when the process exits, including a crash, so a stale file never blocks a
// restart.
func lockRoot(root string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	handle := windows.Handle(file.Fd())
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		_ = file.Close()
		return nil, errors.New("a Forgelet desktop runner is already active on this account")
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
		_ = file.Close()
	}, nil
}
