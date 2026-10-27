package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestActPreparationAndWorkflowHaveIndependentDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name, pullDelay, workerDelay string
		preparation                  time.Duration
		want                         run.State
		workerStarts                 bool
	}{
		{"preparation exceeds workflow budget", "1.2", "0.05", 3 * time.Second, run.Succeeded, true},
		{"workflow budget still enforced", "1.2", "3", 3 * time.Second, run.TimedOut, true},
		{"preparation deadline", "3", "0", 100 * time.Millisecond, run.TimedOut, false},
		{"preparation cancellation", "3", "0", 3 * time.Second, run.Cancelled, false},
		{"pull fails", "0", "0", 3 * time.Second, run.Failed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "worker-started")
			docker := `#!/bin/sh
case "$1" in
image) exit 1;;
pull) printf '%2048s\n' preparing; sleep "$PREPARATION_DELAY"; test "$PREPARATION_FAIL" != yes;;
ps) exit 0;;
*) exit 99;;
esac
`
			worker := `#!/bin/sh
cat >/dev/null
touch "$PREPARATION_WORKER_MARK"
sleep "$PREPARATION_WORKER_DELAY"
printf '{"result":{"state":"success"}}\n'
`
			for name, body := range map[string]string{"docker": docker, "worker": worker} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DOCKER_HOST", "unix://"+run.ActSocket)
			t.Setenv("PREPARATION_DELAY", tc.pullDelay)
			t.Setenv("PREPARATION_WORKER_DELAY", tc.workerDelay)
			t.Setenv("PREPARATION_WORKER_MARK", marker)
			t.Setenv("PREPARATION_FAIL", map[bool]string{true: "yes", false: "no"}[tc.name == "pull fails"])
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			opts := Options{ActImage: "registry.example/tools@sha256:" + strings.Repeat("a", 64), Workspace: filepath.Join(dir, "workspace"), ActWorker: filepath.Join(dir, "worker"), ActPreparationTimeout: tc.preparation}
			if tc.name == "preparation cancellation" {
				opts.Log = func(_ string, _ []byte) error { cancel(); return nil }
			}
			result, err := executeAct(ctx, run.Plan{Kernel: "act", TimeoutSeconds: 1}, opts)
			if result.State != tc.want {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if tc.want == run.Succeeded && err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(marker)
			if (statErr == nil) != tc.workerStarts {
				t.Fatalf("worker start=%v, wanted %v", statErr, tc.workerStarts)
			}
			if tc.name == "preparation deadline" && !strings.Contains(result.Error, "preparation") {
				t.Fatal("missing preparation diagnostic", result)
			}
		})
	}
}
