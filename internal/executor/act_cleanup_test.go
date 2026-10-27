package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStopActContainersConfirmsStoppedWithoutRemovingFilesystems(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr bool
	}{
		{"empty", false}, {"running", false}, {"exited-before-kill", false}, {"delayed-state-update", false}, {"kill-request-pending", false},
		{"list-error", true}, {"kill-error", true}, {"still-running", true}, {"confirm-error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$ACT_CLEANUP_CALLS"
case "$1" in
ps)
 test "$2" = -q || exit 80
 test "$ACT_CLEANUP_MODE" != list-error || exit 81
 test "$ACT_CLEANUP_MODE" != empty || exit 0
 if test -f "$ACT_CLEANUP_MARK"; then
  test "$ACT_CLEANUP_MODE" != confirm-error || exit 82
  if test "$ACT_CLEANUP_MODE" = delayed-state-update && test ! -f "$ACT_CLEANUP_MARK.observed"; then
   touch "$ACT_CLEANUP_MARK.observed"; printf 'job-container\n'
  fi
  case "$ACT_CLEANUP_MODE" in kill-error|still-running) printf 'job-container\n';; esac
 else
  printf 'job-container\nservice-container\n'
 fi;;
kill)
 touch "$ACT_CLEANUP_MARK"
 test "$ACT_CLEANUP_MODE" != kill-request-pending || exec sleep 6
 case "$ACT_CLEANUP_MODE" in kill-error|exited-before-kill) exit 83;; esac;;
*) exit 84;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("ACT_CLEANUP_MODE", tc.name)
			t.Setenv("ACT_CLEANUP_MARK", filepath.Join(dir, "stopped"))
			calls := filepath.Join(dir, "calls")
			t.Setenv("ACT_CLEANUP_CALLS", calls)
			budget := 2 * time.Second
			if tc.name == "kill-request-pending" {
				budget = 7 * time.Second
			}
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			defer cancel()
			if err := stopActContainers(ctx); (err != nil) != tc.wantErr {
				t.Fatalf("cleanup error=%v, wantErr=%v", err, tc.wantErr)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "rm ") {
				t.Fatal("cleanup waits for filesystem removal")
			}
			if tc.name == "running" && string(data) != "ps -q\nkill job-container service-container\nps -q\n" {
				t.Fatal(string(data))
			}
		})
	}
}

func TestStopActContainersHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	if err := stopActContainers(ctx); err == nil {
		t.Fatal("ignored cancelled cleanup")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancelled cleanup blocked")
	}
}
