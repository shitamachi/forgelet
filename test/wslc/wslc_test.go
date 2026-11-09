//go:build windows && wslc

// Package wslc_test verifies the Windows desktop runner against the real
// wslc.exe container CLI and a minimal control plane. It is a manual
// integration test: `go test -tags=wslc ./test/wslc`.
package wslc_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

const imageTag = "forgelet-runner-wslc:itest"

var imageOnce sync.Once

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func mustCommand(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), env...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
}

// buildImage cross-compiles the Linux executor and builds the product image
// with `wslc build`, the same command the packaging script uses.
func buildImage(t *testing.T) {
	t.Helper()
	imageOnce.Do(func() {
		root := repoRoot(t)
		mustCommand(t, root, []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"},
			"go", "build", "-trimpath", "-o", filepath.Join(root, "desktop", "windows", "image", "forgelet-runner"), "./cmd/runner")
		mustCommand(t, root, nil, wslcPath(t), "build", "-t", imageTag,
			"-f", filepath.Join(root, "desktop", "windows", "image", "Dockerfile"),
			filepath.Join(root, "desktop", "windows", "image"))
	})
}

func wslcPath(t *testing.T) string {
	t.Helper()
	candidates := []string{filepath.Join(os.Getenv("ProgramFiles"), "WSL", "wslc.exe")}
	if path, err := exec.LookPath("wslc.exe"); err == nil {
		candidates = append(candidates, path)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	t.Skip("wslc.exe is not installed")
	return ""
}

// certificate generates a CA and a leaf for host.wslc.internal, the name WSLC
// containers use to reach the Windows host.
func certificate(t *testing.T, dir string) (caPath string, leaf tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "forgelet wslc test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "host.wslc.internal"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"host.wslc.internal", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPath = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return caPath, tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: key}
}

// controlPlane answers the runner protocol for one attempt. It records what the
// container executed and whether it uploaded a terminal result.
type controlPlane struct {
	jobID      string
	runID      string
	script     string
	cancel     atomic.Bool
	cancelSeen atomic.Bool
	logs       strings.Builder
	mu         sync.Mutex
	result     atomic.Value
	logSeq     atomic.Int64
}

func (p *controlPlane) plan() run.Plan {
	return run.Plan{
		JobID: p.jobID, RunID: p.runID, Key: p.jobID, Repository: "me/wslc",
		SHA: strings.Repeat("a", 40), Ref: "refs/heads/main", SourceWorkflow: ".github/workflows/wslc.yml",
		External: true, Kernel: "native",
		Steps: []run.Step{{ID: "run", Name: "run", Shell: "bash", Run: p.script}},
	}
}

func (p *controlPlane) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/runner/v1/poll", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, run.ExternalGrant{JobID: p.jobID, RunID: p.runID, Repository: "me/wslc",
			Name: "wslc", SHA: strings.Repeat("a", 40), Seconds: 60})
	})
	mux.HandleFunc("/runner/v1/renew", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"cancel": p.cancel.Load(), "terminal": false, "lease_seconds": 60})
	})
	mux.HandleFunc("/runner/v1/jobs/"+p.jobID+"/claim", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, p.plan())
	})
	mux.HandleFunc("/runner/v1/jobs/"+p.jobID+"/control", func(w http.ResponseWriter, r *http.Request) {
		if p.cancel.Load() {
			p.cancelSeen.Store(true)
		}
		writeJSON(w, map[string]bool{"cancel_requested": p.cancel.Load()})
	})
	mux.HandleFunc("/runner/v1/jobs/"+p.jobID+"/logs", func(w http.ResponseWriter, r *http.Request) {
		var entry run.Log
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		p.mu.Lock()
		p.logs.WriteString(entry.Data)
		p.mu.Unlock()
		p.logSeq.Store(entry.Seq)
		writeJSON(w, map[string]int64{"seq": entry.Seq})
	})
	mux.HandleFunc("/runner/v1/jobs/"+p.jobID+"/result", func(w http.ResponseWriter, r *http.Request) {
		var result run.Result
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		p.result.Store(result.State)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	})
	mux.HandleFunc("/runner/v1/jobs/"+p.jobID+"/progress", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (p *controlPlane) snapshot() (logs string, state run.State) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if value := p.result.Load(); value != nil {
		state = value.(run.State)
	}
	return p.logs.String(), state
}

// runAttempt starts the Windows runner binary with the desktop protocol and
// waits until it reports one of stopAt (by default a terminal job state).
func runAttempt(t *testing.T, url, controlURL, caPath string, timeout time.Duration, stopAt ...string) (string, string) {
	t.Helper()
	if len(stopAt) == 0 {
		stopAt = []string{"succeeded", "failed", "stopped", "timed_out", "error"}
	}
	stop := map[string]bool{}
	for _, state := range stopAt {
		stop[state] = true
	}
	root := repoRoot(t)
	binary := filepath.Join(t.TempDir(), "forgelet-runner.exe")
	mustCommand(t, root, []string{"GOOS=windows", "GOARCH=amd64"}, "go", "build", "-o", binary, "./cmd/runner")
	config, err := json.Marshal(map[string]any{
		"url": url, "token": "frr_" + strings.Repeat("b", 64),
		"wslc": map[string]any{"image": imageTag, "cpus": 2, "memory": "2G", "ca_file": caPath, "control_url": controlURL},
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary)
	// Isolate the runner's per-account cache and single-instance lock from a
	// desktop runner that may be active on this machine.
	command.Env = append(os.Environ(), "LOCALAPPDATA="+t.TempDir())
	stderr := &strings.Builder{}
	command.Stderr = stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(append(config, '\n')); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = command.Wait()
	}()
	var logs strings.Builder
	var state, message string
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	terminal := false
	for !terminal {
		select {
		case line, ok := <-lines:
			if !ok {
				terminal = true
				break
			}
			var update struct {
				State   string `json:"state"`
				Message string `json:"message"`
				Log     *struct {
					Data string `json:"data"`
				} `json:"log"`
			}
			if json.Unmarshal([]byte(line), &update) != nil {
				continue
			}
			if update.Log != nil {
				logs.WriteString(update.Log.Data)
			}
			if update.State != "" {
				state = update.State
				message = update.Message
				t.Logf("status %q %q", state, message)
				if stop[update.State] {
					terminal = true
				}
			}
		case <-ctx.Done():
			_ = command.Process.Kill()
			t.Fatalf("runner timed out: state=%q message=%q logs=%q stderr=%q", state, message, logs.String(), stderr.String())
		}
	}
	_ = stdin.Close()
	select {
	case <-done:
	case <-ctx.Done():
		_ = command.Process.Kill()
		t.Fatalf("runner did not stop: state=%q message=%q logs=%q", state, message, logs.String())
	}
	if stderr.Len() > 0 {
		t.Logf("runner stderr:\n%s", stderr.String())
	}
	return state, logs.String()
}

func TestWSLCExecutesJobAndCleansUp(t *testing.T) {
	wslc := wslcPath(t)
	buildImage(t)
	caPath, leaf := certificate(t, t.TempDir())
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	plane := &controlPlane{jobID: "wslcjob01", runID: "wslcrun01", script: "echo wslc-integration-ok"}
	server := &http.Server{Handler: plane.handler(t), TLSConfig: &tls.Config{Certificates: []tls.Certificate{leaf}}}
	go func() { _ = server.ServeTLS(listener, "", "") }()
	defer func() { _ = server.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("https://127.0.0.1:%d", port)
	control := fmt.Sprintf("https://host.wslc.internal:%d", port)

	state, logs := runAttempt(t, url, control, caPath, 5*time.Minute)
	if state != "succeeded" {
		t.Fatalf("runner state %q, logs %q", state, logs)
	}
	if !strings.Contains(logs, "wslc-integration-ok") {
		t.Fatalf("desktop log missing job output: %q", logs)
	}
	serverLogs, result := plane.snapshot()
	if result != run.Succeeded || !strings.Contains(serverLogs, "wslc-integration-ok") {
		t.Fatalf("control plane result %q logs %q", result, serverLogs)
	}
	// The container is removed after the attempt.
	output, err := exec.Command(wslc, "list", "--all", "--format", "json", "--filter", "label=forgelet.owner=forgelet-runner").Output()
	if err != nil {
		t.Fatal(err)
	}
	if names := parseNames(t, output); len(names) != 0 {
		t.Fatalf("containers left after success: %v", names)
	}
}

func TestWSLCCancelsRunningJob(t *testing.T) {
	wslc := wslcPath(t)
	buildImage(t)
	caPath, leaf := certificate(t, t.TempDir())
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	plane := &controlPlane{jobID: "wslcjob02", runID: "wslcrun02", script: "echo started; sleep 20; echo never"}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plane.handler(t).ServeHTTP(w, r)
		if strings.HasSuffix(r.URL.Path, "/logs") && plane.logSeq.Load() > 0 {
			plane.cancel.Store(true)
		}
	}), TLSConfig: &tls.Config{Certificates: []tls.Certificate{leaf}}}
	go func() { _ = server.ServeTLS(listener, "", "") }()
	defer func() { _ = server.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("https://127.0.0.1:%d", port)
	control := fmt.Sprintf("https://host.wslc.internal:%d", port)

	state, logs := runAttempt(t, url, control, caPath, 4*time.Minute)
	if state != "stopped" {
		t.Fatalf("runner state %q, logs %q", state, logs)
	}
	_, result := plane.snapshot()
	if result != run.Cancelled {
		t.Fatalf("control plane result %q", result)
	}
	output, err := exec.Command(wslc, "list", "--all", "--format", "json", "--filter", "label=forgelet.owner=forgelet-runner").Output()
	if err != nil {
		t.Fatal(err)
	}
	if names := parseNames(t, output); len(names) != 0 {
		t.Fatalf("containers left after cancel: %v", names)
	}
}

// TestWSLCRemovesOrphanedContainers covers a killed supervisor: the next start
// lists containers by the ownership label and removes them before claiming work.
func TestWSLCRemovesOrphanedContainers(t *testing.T) {
	wslc := wslcPath(t)
	buildImage(t)
	orphan := "forgelet-orphaned-attempt"
	_ = exec.Command(wslc, "remove", "--force", orphan).Run()
	out, err := exec.Command(wslc, "run", "-d", "--name", orphan,
		"--label", "forgelet.owner=forgelet-runner", "alpine:3.20", "sleep", "120").CombinedOutput()
	if err != nil {
		t.Fatalf("create orphan: %v\n%s", err, out)
	}
	defer func() { _ = exec.Command(wslc, "remove", "--force", orphan).Run() }()

	caPath, leaf := certificate(t, t.TempDir())
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is ever offered, so the runner reports its cleaned state and waits.
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), TLSConfig: &tls.Config{Certificates: []tls.Certificate{leaf}}}
	go func() { _ = server.ServeTLS(listener, "", "") }()
	defer func() { _ = server.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port

	state, logs := runAttempt(t,
		fmt.Sprintf("https://127.0.0.1:%d", port),
		fmt.Sprintf("https://host.wslc.internal:%d", port), caPath, 2*time.Minute, "available")
	if state != "available" {
		t.Fatalf("runner reported %q, logs %q", state, logs)
	}
	output, err := exec.Command(wslc, "list", "--all", "--format", "json", "--filter", "label=forgelet.owner=forgelet-runner").Output()
	if err != nil {
		t.Fatal(err)
	}
	if names := parseNames(t, output); len(names) != 0 {
		t.Fatalf("orphaned containers survived startup: %v", names)
	}
}

func parseNames(t *testing.T, data []byte) []string {
	t.Helper()
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	type entry struct {
		Names string `json:"Names"`
	}
	var entries []entry
	if trimmed[0] == '[' {
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			t.Fatalf("wslc list: %v (%s)", err, data)
		}
	} else {
		var single entry
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			t.Fatalf("wslc list: %v (%s)", err, data)
		}
		entries = []entry{single}
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Names)
	}
	return names
}
