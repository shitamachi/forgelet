//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"

	"github.com/shitamachi/forgelet/internal/delivery"
	"github.com/shitamachi/forgelet/internal/run"
)

func clusterCommand(t *testing.T, ctx context.Context, cluster testcontainers.Container, args ...string) string {
	t.Helper()
	code, output, err := cluster.Exec(ctx, args)
	must(t, err)
	data, err := io.ReadAll(output)
	must(t, err)
	if code != 0 {
		t.Fatalf("cluster command %v exit %d: %s", args, code, data)
	}
	return string(data)
}
func applyInCluster(t *testing.T, ctx context.Context, cluster testcontainers.Container, data []byte) {
	t.Helper()
	must(t, cluster.CopyToContainer(ctx, data, "/tmp/forgelet-test-apply.yaml", 0600))
	clusterCommand(t, ctx, cluster, "kubectl", "apply", "--server-side", "--field-manager=forgelet-test", "-f", "/tmp/forgelet-test-apply.yaml")
}
func installFlux(t *testing.T, ctx context.Context, cluster testcontainers.Container) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, "GET", "https://github.com/fluxcd/flux2/releases/download/v2.6.4/install.yaml", nil)
	must(t, err)
	client := &http.Client{Timeout: time.Minute}
	response, err := client.Do(request)
	must(t, err)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		t.Fatalf("download Flux: %d", response.StatusCode)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(io.LimitReader(response.Body, 4<<20), 4096)
	var manifest bytes.Buffer
	for {
		var object map[string]any
		err := decoder.Decode(&object)
		if errors.Is(err, io.EOF) {
			break
		}
		must(t, err)
		if len(object) == 0 {
			continue
		}
		kind, _ := object["kind"].(string)
		metadata := object["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		if kind == "Deployment" && (name != "source-controller" && name != "kustomize-controller") {
			continue
		}
		if kind == "Deployment" {
			containers := object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
			containers[0].(map[string]any)["resources"] = map[string]any{"requests": map[string]string{"cpu": "50m", "memory": "64Mi"}, "limits": map[string]string{"cpu": "1", "memory": "512Mi"}}
		}
		data, err := json.Marshal(object)
		must(t, err)
		manifest.WriteString("---\n")
		manifest.Write(data)
		manifest.WriteByte('\n')
	}
	applyInCluster(t, ctx, cluster, manifest.Bytes())
	clusterCommand(t, ctx, cluster, "kubectl", "-n", "flux-system", "rollout", "status", "deployment/source-controller", "--timeout=180s")
	clusterCommand(t, ctx, cluster, "kubectl", "-n", "flux-system", "rollout", "status", "deployment/kustomize-controller", "--timeout=180s")
}

func testFluxDelivery(t *testing.T, ctx context.Context, cluster testcontainers.Container, client *kubernetes.Clientset, httpClient *http.Client, endpoint, root, image, goodDigest, source string, request func(*testing.T, string, string, any, int) []byte, create func(*testing.T, string) run.Run, get func(*testing.T, string) run.Run) {
	t.Helper()
	installFlux(t, ctx, cluster)
	server, err := client.AppsV1().Deployments("forgelet").Get(ctx, "forgelet", metav1.GetOptions{})
	must(t, err)
	executorImage := server.Spec.Template.Spec.Containers[0].Image
	script, err := os.ReadFile(filepath.Join(root, "test/fixtures/gitops.js"))
	must(t, err)
	_, err = client.CoreV1().ConfigMaps("forgelet").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "git-fixture"}, Data: map[string]string{"gitops.js": string(script)}}, metav1.CreateOptions{})
	must(t, err)
	_, err = client.CoreV1().Pods("forgelet").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "git-fixture", Labels: map[string]string{"app": "git-fixture"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "git", Image: executorImage, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(9443), Scheme: corev1.URISchemeHTTPS}}, PeriodSeconds: 1}, Command: []string{"node", "/fixture/gitops.js"}, VolumeMounts: []corev1.VolumeMount{{Name: "script", MountPath: "/fixture", ReadOnly: true}, {Name: "tls", MountPath: "/tls", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "script", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "git-fixture"}}}}, {Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "forgelet-api-tls"}}}}}}, metav1.CreateOptions{})
	must(t, err)
	_, err = client.CoreV1().Services("forgelet").Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "git"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "git-fixture"}, Ports: []corev1.ServicePort{{Port: 9443, TargetPort: intstr.FromInt32(9443)}}}}, metav1.CreateOptions{})
	must(t, err)
	clusterCommand(t, ctx, cluster, "kubectl", "wait", "-n", "forgelet", "pod/git-fixture", "--for=condition=Ready", "--timeout=90s")
	seed, _ := json.Marshal(map[string]string{"image": image, "digest": goodDigest})
	js := `const https=require('https'),fs=require('fs');const body=process.argv[1];const r=https.request({hostname:'localhost',port:9443,path:'/seed',method:'POST',ca:fs.readFileSync('/tls/tls.crt')},s=>{s.pipe(process.stdout);s.on('end',()=>process.exit(s.statusCode===200?0:1));});r.on('error',e=>{console.error(e.message);process.exit(1)});r.end(body);`
	clusterCommand(t, ctx, cluster, "kubectl", "exec", "-n", "forgelet", "git-fixture", "--", "node", "-e", js, string(seed))
	// containerd reads per-registry hosts dynamically; HTTP is confined to this
	// disposable registry. The production profile defaults to registry TLS.
	registry := strings.SplitN(image, "/", 2)[0]
	clusterCommand(t, ctx, cluster, "mkdir", "-p", "/var/lib/rancher/k3s/agent/etc/containerd/certs.d/"+registry)
	hosts := fmt.Sprintf("server = \"http://%s\"\n[host.\"http://%s\"]\n  capabilities = [\"pull\", \"resolve\"]\n", registry, registry)
	must(t, cluster.CopyToContainer(ctx, []byte(hosts), "/var/lib/rancher/k3s/agent/etc/containerd/certs.d/"+registry+"/hosts.toml", 0600))
	ca, err := client.CoreV1().Secrets("forgelet").Get(ctx, "forgelet-api-ca", metav1.GetOptions{})
	must(t, err)
	_, err = client.CoreV1().Secrets("forgelet").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-ca"}, Data: map[string][]byte{"ca.crt": ca.Data["ca.crt"]}}, metav1.CreateOptions{})
	must(t, err)
	applyInCluster(t, ctx, cluster, []byte(`apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata: {name: phase3, namespace: forgelet}
spec:
  interval: 2s
  url: https://git.forgelet.svc:9443/test/gitops.git
  ref: {branch: main}
  secretRef: {name: git-ca}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: phase3, namespace: forgelet}
spec:
  interval: 2s
  retryInterval: 2s
  timeout: 10s
  path: ./apps
  prune: true
  wait: true
  sourceRef: {kind: GitRepository, name: phase3}
`))
	_, err = client.RbacV1().Roles("forgelet").Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "delivery-observer"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get"}}, {APIGroups: []string{"kustomize.toolkit.fluxcd.io"}, Resources: []string{"kustomizations"}, Verbs: []string{"get"}}, {APIGroups: []string{"source.toolkit.fluxcd.io"}, Resources: []string{"gitrepositories"}, Verbs: []string{"get"}}}}, metav1.CreateOptions{})
	must(t, err)
	_, err = client.RbacV1().RoleBindings("forgelet").Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "delivery-observer"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "forgelet-server", Namespace: "forgelet"}}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "delivery-observer"}}, metav1.CreateOptions{})
	must(t, err)
	cm, err := client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
	must(t, err)
	cm.Data["forgelet.yaml"] += fmt.Sprintf("\ntargets:\n  app:\n    source_repository: test/project\n    source_ref: refs/heads/main\n    repository: test/gitops\n    installation_id: 7\n    branch: main\n    path: apps/kustomization.yaml\n    mode: direct\n    image: %s\n    image_name: app\n    git_url: https://git.forgelet.svc:9443/test/gitops.git\n    flux_namespace: forgelet\n    kustomization: phase3\n    namespace: forgelet\n    deployment: phase3-app\n    container: app\n", image)
	_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
	must(t, err)
	restartServer(t, ctx, client, httpClient, endpoint)
	deliver := func(digest string) delivery.Record {
		item := create(t, fmt.Sprintf("on: push\njobs:\n  deliver:\n    runs-on: homelab\n    steps:\n      - uses: forgelet/deploy@v1\n        with: {target: app, digest: '%s'}\n", digest))
		if finished := finishRun(t, ctx, get, item.ID); finished.State != run.Succeeded {
			t.Fatalf("submit delivery: %+v", finished)
		}
		var record delivery.Record
		must(t, json.Unmarshal(request(t, "GET", "/api/deployments/"+delivery.ID(item.ID, "app"), nil, 200), &record))
		return record
	}
	read := func(id string) delivery.Record {
		var record delivery.Record
		must(t, json.Unmarshal(request(t, "GET", "/api/deployments/"+id, nil, 200), &record))
		return record
	}
	good := deliver(goodDigest)
	waitFor(t, ctx, "Flux applies actual Git revision and healthy digest", func() bool { return read(good.ID).State == delivery.Deployed })
	confirmed := read(good.ID)
	if confirmed.Commit == "" || confirmed.ObservedRevision != confirmed.Revision {
		t.Fatal(confirmed)
	}
	// Build a valid image whose application fails. CI success must not imply CD health.
	badSource := strings.Replace(source, `CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]`, `CMD ["false"]`, 1)
	badBuild := create(t, badSource)
	badResult := finishRun(t, ctx, get, badBuild.ID)
	if badResult.State != run.Succeeded {
		t.Fatal(badResult)
	}
	bad := deliver(badResult.Jobs[0].Result.Outputs["digest"])
	waitFor(t, ctx, "unhealthy image is actually rolled out", func() bool {
		d, err := client.AppsV1().Deployments("forgelet").Get(ctx, "phase3-app", metav1.GetOptions{})
		if err != nil || d.Spec.Template.Spec.Containers[0].Image != image+"@"+bad.Digest || d.Status.ObservedGeneration != d.Generation {
			return false
		}
		pods, err := client.CoreV1().Pods("forgelet").List(ctx, metav1.ListOptions{LabelSelector: "app=phase3-app"})
		if err != nil {
			return false
		}
		for _, pod := range pods.Items {
			if pod.Spec.Containers[0].Image != image+"@"+bad.Digest {
				continue
			}
			for _, status := range pod.Status.ContainerStatuses {
				if status.LastTerminationState.Terminated != nil && status.LastTerminationState.Terminated.ExitCode != 0 {
					return true
				}
			}
		}
		return false
	})
	if got := read(bad.ID); got.State != delivery.Delivered {
		t.Fatalf("unhealthy release falsely deployed: %+v", got)
	}
	restartServer(t, ctx, client, httpClient, endpoint)
	var rollback delivery.Record
	must(t, json.Unmarshal(request(t, "POST", "/api/deployments/"+bad.ID+"/rollback", nil, 200), &rollback))
	waitFor(t, ctx, "rollback commit restored healthy application", func() bool { return read(rollback.ID).State == delivery.Deployed })
	if got := read(rollback.ID); got.Digest != goodDigest || got.Commit == bad.Commit || got.RollbackOf != bad.ID {
		t.Fatal(got)
	}
	waitFor(t, ctx, "failed deployment superseded", func() bool { return read(bad.ID).State == delivery.Superseded })
	cm, err = client.CoreV1().ConfigMaps("forgelet").Get(ctx, "forgelet-config", metav1.GetOptions{})
	must(t, err)
	cm.Data["forgelet.yaml"] = strings.Replace(cm.Data["forgelet.yaml"], "    mode: direct", "    mode: pr", 1)
	_, err = client.CoreV1().ConfigMaps("forgelet").Update(ctx, cm, metav1.UpdateOptions{})
	must(t, err)
	restartServer(t, ctx, client, httpClient, endpoint)
	prDelivery := deliver(goodDigest)
	waitFor(t, ctx, "PR waits for merge", func() bool { return read(prDelivery.ID).State == delivery.AwaitingMerge })
	pending := read(prDelivery.ID)
	if pending.PR <= 0 || pending.Revision != "" {
		t.Fatal(pending)
	}
	restartServer(t, ctx, client, httpClient, endpoint)
	mergeJS := `const https=require('https'),fs=require('fs');const r=https.request({hostname:'localhost',port:9443,path:'/merge/'+process.argv[1],method:'POST',ca:fs.readFileSync('/tls/tls.crt')},s=>{s.pipe(process.stdout);s.on('end',()=>process.exit(s.statusCode===200?0:1));});r.on('error',()=>process.exit(1));r.end('{}');`
	clusterCommand(t, ctx, cluster, "kubectl", "exec", "-n", "forgelet", "git-fixture", "--", "node", "-e", mergeJS, fmt.Sprint(pending.PR))
	waitFor(t, ctx, "Flux reports actual merged PR revision", func() bool { return read(prDelivery.ID).State == delivery.Deployed })
	merged := read(prDelivery.ID)
	if merged.Commit == merged.Revision || merged.Revision != merged.ObservedRevision {
		t.Fatalf("PR head mistaken for merged revision: %+v", merged)
	}

}
