//go:build integration

package integration_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/server"
	"github.com/shitamachi/forgelet/internal/storage"
)

func TestK3sPodScheduleTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cluster, err := k3s.Run(ctx, "rancher/k3s:v1.34.4-k3s1",
		testcontainers.WithCmdArgs("--disable=metrics-server,coredns,local-storage"))
	if cluster != nil {
		testcontainers.CleanupContainer(t, cluster)
	}
	must(t, err)
	kubeconfig, err := cluster.GetKubeConfig(ctx)
	must(t, err)
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	must(t, err)
	client, err := kubernetes.NewForConfig(config)
	must(t, err)
	const namespace = "forgelet"
	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{})
	must(t, err)
	_, err = client.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: kube.ServiceAccount}}, metav1.CreateOptions{})
	must(t, err)
	runtime := &kube.Runtime{Client: client, Namespace: namespace, ServerURL: "https://forgelet.forgelet.svc"}
	dir := t.TempDir()
	path := filepath.Join(dir, "forgelet.db")
	cfg := server.Config{AdminAccess: "open", MaxParallel: 1, DataDir: dir, PodScheduleTimeout: 5 * time.Second,
		Repositories: []string{"test/project"}, Profiles: map[string]kube.Profile{
			"blocked": {Image: "unused.invalid/executor:test", NodeSelector: map[string]string{"forgelet.dev/test-impossible": "absent"}},
		}}
	open := func() (*storage.Store, *server.Server) {
		db, err := storage.Open(ctx, path)
		must(t, err)
		app, err := server.New(cfg, db, runtime, nil)
		if err == nil {
			err = app.LoadManagement(ctx)
		}
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		return db, app
	}

	db, app := open()
	defer func() { _ = db.Close() }()
	for _, id := range []string{"first", "next"} {
		_, err := db.CreateRun(ctx, run.Run{ID: id, DeliveryID: id, WorkflowPath: "ci.yml", Repository: "test/project"},
			[]run.Plan{{JobID: id, RunID: id, Key: "blocked", Name: "blocked", Profile: "blocked", TimeoutSeconds: 120}})
		must(t, err)
	}
	must(t, app.Tick(ctx))
	pods := client.CoreV1().Pods(namespace)
	waitFor(t, ctx, "scheduler rejected the test Pod", func() bool {
		pod, err := pods.Get(ctx, "f-first", metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse &&
				condition.Reason == corev1.PodReasonUnschedulable {
				return true
			}
		}
		return false
	})
	must(t, db.Close())
	db, app = open()
	waitFor(t, ctx, "timeout releases the Pod and reports scheduler reason", func() bool {
		must(t, app.Tick(ctx))
		job, err := db.GetJob(ctx, "first")
		must(t, err)
		_, err = pods.Get(ctx, "f-first", metav1.GetOptions{})
		return job.State == run.Interrupted && job.Result != nil &&
			strings.Contains(job.Result.Error, "5s: Unschedulable") && apierrors.IsNotFound(err)
	})
	next, err := db.GetJob(ctx, "next")
	must(t, err)
	if next.State != run.Starting || next.PodUID == "" {
		t.Fatalf("next job did not reuse the released slot: %+v", next)
	}
}
