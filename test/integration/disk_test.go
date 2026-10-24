//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/server"
)

const testStorageRoot = "/var/lib/rancher/k3s/storage"

func testDiskPressure(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint string, request func(*testing.T, string, string, any, int) []byte, create func(*testing.T, string) run.Run, get func(*testing.T, string) run.Run) {
	t.Run("disk pressure pauses and resumes scheduling", func(t *testing.T) {
		status := func() server.DiskStatus {
			var value struct {
				Disk server.DiskStatus `json:"disk"`
			}
			must(t, json.Unmarshal(request(t, "GET", "/api/status", nil, 200), &value))
			return value.Disk
		}
		waitFor(t, ctx, "initial healthy disk", func() bool { return !status().Paused })
		initial := status()
		if initial.AvailableBytes == nil || *initial.AvailableBytes > 256<<20 || initial.PauseBelowBytes != 32<<20 || initial.ResumeAtBytes != 64<<20 {
			t.Logf("storage filesystem: %s", clusterCommand(t, ctx, cluster, "df", "-Pk", testStorageRoot))
			if initial.AvailableBytes != nil {
				t.Logf("server reports %d available bytes", *initial.AvailableBytes)
			}
			t.Fatalf("pressure fixture must use the bounded PVC filesystem: %+v", initial)
		}
		var created []string
		pressureFile := testStorageRoot + "/forgelet-pressure-test"
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cancel()
			clusterCommand(t, cleanupCtx, cluster, "rm", "-f", pressureFile)
			if t.Failed() {
				for _, id := range created {
					api(t, cleanupCtx, httpClient, endpoint, "POST", "/api/runs/"+id+"/cancel", nil, token, 200)
				}
			}
		})
		start := func(source string) run.Run {
			item := create(t, source)
			created = append(created, item.ID)
			return item
		}
		live := start(`on: push
jobs:
  active:
    runs-on: homelab
    steps:
      - run: |
          echo before-disk-pressure
          while [ ! -f /workspace/pressure-release ]; do sleep 1; done
          echo finished-under-disk-pressure
  dependent:
    needs: active
    runs-on: homelab
    steps: [{run: echo dependent-resumed}]
`)
		cancelled := start("on: push\njobs:\n  cancel:\n    runs-on: homelab\n    steps: [{run: 'sleep 300'}]\n")
		waitFor(t, ctx, "two live attempts before pressure", func() bool {
			return jobByKey(t, get(t, live.ID), "active").State == run.Running && get(t, cancelled.ID).State == run.Running
		})
		original := jobByKey(t, get(t, live.ID), "active")
		queued := start(`on: push
jobs:
  one:
    runs-on: homelab
    steps: [{run: 'sleep 2; echo resumed-one'}]
  two:
    runs-on: homelab
    steps: [{run: 'sleep 2; echo resumed-two'}]
  three:
    runs-on: homelab
    steps: [{run: 'sleep 2; echo resumed-three'}]
`)
		discarded := start("on: push\njobs:\n  cancelled:\n    runs-on: homelab\n    steps: [{run: echo must-not-run}]\n")
		// Allocate actual pages, leaving 16–17 MiB free. This cannot write beyond
		// the dedicated 256 MiB tmpfs mounted on the disposable k3s container.
		clusterCommand(t, ctx, cluster, "sh", "-ceu", `available=$(df -Pk "$1" | awk 'NR==2 {print $4}')
count=$((available / 1024 - 16))
test "$count" -gt 0
dd if=/dev/zero of="$2" bs=1048576 count="$count"`, "pressure", testStorageRoot, pressureFile)
		waitFor(t, ctx, "low space pauses admission", func() bool {
			s := status()
			return s.Paused && s.Reason == "disk_low" && s.AvailableBytes != nil && *s.AvailableBytes < s.PauseBelowBytes
		})
		request(t, "GET", "/healthz", nil, 200)
		restartServer(t, ctx, client, httpClient, endpoint)
		waitFor(t, ctx, "restart remains paused", func() bool { return status().Reason == "disk_low" })
		adopted := jobByKey(t, get(t, live.ID), "active")
		pod, err := client.CoreV1().Pods("forgelet").Get(ctx, original.PodName, metav1.GetOptions{})
		must(t, err)
		if adopted.PodUID != original.PodUID || string(pod.UID) != original.PodUID || adopted.State != run.Running {
			t.Fatalf("pressure restart replaced running attempt: original=%+v adopted=%+v", original, adopted)
		}
		clusterCommand(t, ctx, cluster, "kubectl", "-n", "forgelet", "exec", original.PodName, "--", "touch", "/workspace/pressure-release")
		request(t, "POST", "/api/runs/"+cancelled.ID+"/cancel", nil, 200)
		request(t, "POST", "/api/runs/"+discarded.ID+"/cancel", nil, 200)
		waitFor(t, ctx, "results and cancellation finish while paused", func() bool {
			return jobByKey(t, get(t, live.ID), "active").State == run.Succeeded && get(t, cancelled.ID).State == run.Cancelled && get(t, discarded.ID).State == run.Cancelled
		})
		if logs := request(t, "GET", "/api/jobs/"+original.ID+"/logs", nil, 200); !strings.Contains(string(logs), "finished-under-disk-pressure") {
			t.Fatalf("logs lost while paused: %s", logs)
		}
		waitFor(t, ctx, "Pod GC continues under pressure", func() bool {
			pods, err := client.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{LabelSelector: "forgelet.dev/job"})
			return err == nil && len(pods.Items) == 0
		})
		assertQueued := func() {
			t.Helper()
			previous := status().CheckedAt
			for range 3 {
				waitFor(t, ctx, "paused scheduling tick", func() bool { return status().CheckedAt.After(previous) })
				s := status()
				previous = s.CheckedAt
				if !s.Paused {
					t.Fatalf("unexpected resume: %+v", s)
				}
				jobs := append(get(t, queued.ID).Jobs, jobByKey(t, get(t, live.ID), "dependent"))
				for _, j := range jobs {
					if j.State != run.Queued || j.PodUID != "" {
						t.Fatalf("queued attempt admitted under pressure: %+v", j)
					}
					_, err := client.CoreV1().Pods("forgelet").Get(ctx, j.PodName, metav1.GetOptions{})
					if !apierrors.IsNotFound(err) {
						t.Fatalf("queued attempt has a Pod: %s %v", j.ID, err)
					}
				}
			}
		}
		assertQueued()
		// Run the real retention endpoint while paused and observe a filesystem
		// deletion, without touching the database or unexpired application data.
		backup := "/data/backup-" + strings.Repeat("a", 32) + ".db"
		clusterCommand(t, ctx, cluster, "kubectl", "-n", "forgelet", "exec", "deployment/forgelet", "-c", "server", "--", "sh", "-ceu", `dd if=/dev/zero of="$1" bs=1048576 count=4; touch -t 202001010000 "$1"`, "backup", backup)
		var maintenance server.MaintenanceResult
		must(t, json.Unmarshal(request(t, "POST", "/api/maintenance", nil, 200), &maintenance))
		if maintenance.Backups != 1 {
			t.Fatalf("paused maintenance did not reclaim backup: %+v", maintenance)
		}
		clusterCommand(t, ctx, cluster, "kubectl", "-n", "forgelet", "exec", "deployment/forgelet", "-c", "server", "--", "test", "!", "-e", backup)
		// Free 32 MiB in-place: never briefly remove the filler and cross the
		// recovery threshold. Space is now above pause but below resume.
		clusterCommand(t, ctx, cluster, "sh", "-ceu", `size=$(stat -c %s "$1"); truncate -s "$((size - 32 * 1024 * 1024))" "$1"`, "release", pressureFile)
		waitFor(t, ctx, "hysteresis band", func() bool {
			s := status()
			return s.AvailableBytes != nil && *s.AvailableBytes >= s.PauseBelowBytes && *s.AvailableBytes < s.ResumeAtBytes
		})
		assertQueued()
		restartServer(t, ctx, client, httpClient, endpoint)
		assertQueued() // Restart cannot bypass the recovery threshold.
		clusterCommand(t, ctx, cluster, "rm", pressureFile)
		waitFor(t, ctx, "automatic scheduling recovery", func() bool { return !status().Paused })
		seenParallel := false
		waitFor(t, ctx, "queued work completes after recovery", func() bool {
			var items []run.Run
			must(t, json.Unmarshal(request(t, "GET", "/api/runs", nil, 200), &items))
			active := 0
			for _, item := range items {
				for _, j := range item.Jobs {
					if j.State == run.Starting || j.State == run.Running {
						active++
					}
				}
			}
			if active > 2 {
				t.Fatalf("resumption exceeded concurrency limit: %d", active)
			}
			seenParallel = seenParallel || active == 2
			return get(t, queued.ID).State.Terminal() && get(t, live.ID).State.Terminal()
		})
		if get(t, queued.ID).State != run.Succeeded || get(t, live.ID).State != run.Succeeded || !seenParallel {
			t.Fatal("resumed work failed or lost parallelism")
		}
		t.Log("real filesystem pressure: admission paused, running UID preserved across restart, results/cancel/GC/maintenance continued, hysteresis survived restart, queued work resumed at concurrency 2")
	})
}
