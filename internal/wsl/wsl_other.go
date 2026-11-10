//go:build !windows

// Package wsl drives Windows Subsystem for Linux distributions through wsl.exe.
// Only Windows has one, so this file keeps the package buildable elsewhere for
// tools that enumerate all packages.
package wsl

import "errors"

// Find reports that WSL is unavailable on this platform.
func Find() (string, error) { return "", errors.New("WSL 仅 Windows 可用") }
