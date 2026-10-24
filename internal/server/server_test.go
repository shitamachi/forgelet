package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/shitamachi/forgelet/internal/kube"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/storage"
)

const testAdmin = "test-admin-token-at-least-16"
const testSHA = "0123456789012345678901234567890123456789"
const testWorkflow = `name: Shell CI
on: push
jobs:
  test:
    runs-on: small
    steps:
      - id: test
        run: echo hello
`

type fakePod struct {
	job   run.Job
	uid   string
	phase corev1.PodPhase
}

type fakeRuntime struct {
	mu                 sync.Mutex
	pods               map[string]fakePod
	created            int
	deleted            []string
	loseCreateResponse bool
}

func (f *fakeRuntime) Ensure(_ context.Context, job run.Job, _ kube.Profile) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pod, ok := f.pods[job.ID]; ok {
		return pod.uid, nil
	}
	uid := "uid-" + job.ID
	f.pods[job.ID] = fakePod{job: job, uid: uid, phase: corev1.PodRunning}
	f.created++
	if f.loseCreateResponse {
		f.loseCreateResponse = false
		return "", errors.New("Pod created but response lost")
	}
	return uid, nil
}

func (f *fakeRuntime) Observe(_ context.Context, job run.Job) (kube.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pod, ok := f.pods[job.ID]
	if !ok {
		return kube.Observation{Missing: true}, nil
	}
	if job.PodUID != "" && job.PodUID != pod.uid {
		return kube.Observation{}, errors.New("Pod UID mismatch")
	}
	return kube.Observation{UID: pod.uid, Phase: pod.phase}, nil
}

func (f *fakeRuntime) Delete(_ context.Context, job run.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pod, ok := f.pods[job.ID]; ok && pod.uid != job.PodUID {
		return errors.New("Pod UID precondition failed")
	}
	delete(f.pods, job.ID)
	f.deleted = append(f.deleted, job.ID)
	return nil
}

func (f *fakeRuntime) Pods(_ context.Context) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pods := make(map[string]string, len(f.pods))
	for id, p := range f.pods {
		pods[id] = p.uid
	}
	return pods, nil
}

func (f *fakeRuntime) Authenticate(_ context.Context, token string) (kube.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pods[strings.TrimPrefix(token, "token-")]
	if !strings.HasPrefix(token, "token-") || !ok {
		return kube.Identity{}, kube.ErrIdentity
	}
	return kube.Identity{PodName: p.job.PodName, PodUID: p.uid}, nil
}

type fakeProvider struct {
	mu               sync.Mutex
	files            map[string][]byte
	workflowFailures int
	reportFailures   int
	reports          []run.Check
	tokenRequests    []string
}

func (f *fakeProvider) Workflows(_ context.Context, _ run.Event) (map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.workflowFailures > 0 {
		f.workflowFailures--
		return nil, errors.New("temporary GitHub outage")
	}
	return f.files, nil
}

func (f *fakeProvider) InstallationToken(_ context.Context, _ int64) (string, error) {
	return "installation-token", nil
}

func (f *fakeProvider) RepositoryToken(_ context.Context, installationID int64, repository string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenRequests = append(f.tokenRequests, fmt.Sprintf("%d:%s", installationID, repository))
	return "repository-scoped-clone-token", nil
}

func (f *fakeProvider) ReportCheck(_ context.Context, check run.Check, _ string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, check)
	if f.reportFailures > 0 {
		f.reportFailures--
		return 0, errors.New("temporary Checks outage")
	}
	if check.RemoteID != 0 {
		return check.RemoteID, nil
	}
	return 101, nil
}

type harness struct {
	t        *testing.T
	path     string
	cfg      Config
	store    *storage.Store
	runtime  *fakeRuntime
	provider *fakeProvider
	server   *Server
	http     *httptest.Server
}

func newHarness(t *testing.T, parallel int) *harness {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.Open(t.Context(), filepath.Join(dir, "forgelet.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, path: filepath.Join(dir, "forgelet.db"), store: s,
		runtime:  &fakeRuntime{pods: map[string]fakePod{}},
		provider: &fakeProvider{files: map[string][]byte{".github/workflows/ci.yml": []byte(testWorkflow)}},
		cfg: Config{AdminToken: testAdmin, WebhookSecret: "webhook-secret", PublicURL: "https://ci.example.test", MaxParallel: parallel,
			Repositories: []string{"me/repo"}, Profiles: map[string]kube.Profile{"small": {Image: "executor:test"}}, DataDir: dir},
	}
	h.start()
	t.Cleanup(func() {
		h.http.Close()
		if err := h.store.Close(); err != nil {
			t.Error(err)
		}
	})
	return h
}

func (h *harness) start() {
	h.t.Helper()
	var err error
	h.server, err = New(h.cfg, h.store, h.runtime, h.provider)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.server.LoadManagement(h.t.Context()); err != nil {
		h.t.Fatal(err)
	}
	if err := h.server.InitAuth(h.t.Context()); err != nil {
		h.t.Fatal(err)
	}
	// Unit/HTTP tests control disk pressure explicitly; do not depend on how
	// much unrelated data is on the developer's filesystem. K3s uses statfs.
	h.server.disk.probe = func(context.Context, string) (uint64, error) { return 2 << 30, nil }
	h.http = httptest.NewServer(h.server.Handler())
	h.http.Client().Timeout = 5 * time.Second
}

func (h *harness) request(method, path, raw string, headers map[string]string) (int, []byte) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.t.Context(), method, h.http.URL+path, strings.NewReader(raw))
	if err != nil {
		h.t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := h.http.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp.StatusCode, body
}

func (h *harness) admin(method, path string, body any) (int, []byte) {
	h.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.request(method, path, string(data), map[string]string{"Authorization": "Bearer " + testAdmin, "Content-Type": "application/json"})
}

func (h *harness) runtimeRequest(method, suffix string, job run.Job, body any) (int, []byte) {
	h.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.request(method, "/internal/jobs/"+job.ID+"/"+suffix, string(data), map[string]string{"Authorization": "Bearer token-" + job.ID, "Content-Type": "application/json"})
}

func (h *harness) manual(source string) run.Run {
	h.t.Helper()
	code, body := h.admin(http.MethodPost, "/api/runs", map[string]any{"repository": "me/repo", "sha": testSHA, "ref": "refs/heads/main", "workflow": source, "installation_id": 42})
	if code != http.StatusCreated {
		h.t.Fatalf("manual: %d %s", code, body)
	}
	var record run.Run
	if err := json.Unmarshal(body, &record); err != nil {
		h.t.Fatal(err)
	}
	return record
}

func (h *harness) tick() {
	h.t.Helper()
	if err := h.server.Tick(h.t.Context()); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) complete(job run.Job) {
	h.t.Helper()
	for _, req := range []struct {
		path string
		body any
	}{
		{"claim", nil},
		{"logs", run.Log{Seq: 1, StepID: "test", Data: "test passed\n"}},
		{"result", run.Result{State: run.Succeeded, LastLogSeq: 1}},
	} {
		if code, body := h.runtimeRequest(http.MethodPost, req.path, job, req.body); code != http.StatusOK {
			h.t.Fatalf("%s: %d %s", req.path, code, body)
		}
	}
}

func (h *harness) webhook(delivery string, mutate func(map[string]string)) (int, []byte) {
	h.t.Helper()
	body := fmt.Sprintf(`{"ref":"refs/heads/main","after":%q,"repository":{"full_name":"me/repo"},"installation":{"id":42}}`, testSHA)
	mac := hmac.New(sha256.New, []byte(h.cfg.WebhookSecret))
	_, _ = mac.Write([]byte(body))
	headers := map[string]string{"X-Hub-Signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil)), "X-GitHub-Delivery": delivery, "X-GitHub-Event": "push"}
	if mutate != nil {
		mutate(headers)
	}
	return h.request(http.MethodPost, "/webhooks/github", body, headers)
}

func TestAuthenticationAndStrictJSON(t *testing.T) {
	h := newHarness(t, 1)
	for _, path := range []string{"/api/runs", "/api/events", "/api/jobs/missing/logs"} {
		if code, _ := h.request(http.MethodGet, path, "", nil); code != http.StatusUnauthorized {
			t.Errorf("anonymous %s=%d", path, code)
		}
	}
	if code, _ := h.request(http.MethodGet, "/healthz", "", nil); code != http.StatusOK {
		t.Fatalf("health=%d", code)
	}
	for _, raw := range []string{`{`, `{} {}`, `{"unknown":true}`, `null`} {
		if code, _ := h.request(http.MethodPost, "/api/runs", raw, map[string]string{"Authorization": "Bearer " + testAdmin}); code != http.StatusBadRequest {
			t.Errorf("invalid JSON %q=%d", raw, code)
		}
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/runs", nil)
	req.SetBasicAuth("admin", testAdmin)
	w := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("browser basic read=%d", w.Code)
	}
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/runs", strings.NewReader(`{}`))
	req.SetBasicAuth("admin", testAdmin)
	w = httptest.NewRecorder()
	h.server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("ambient basic credentials authorized mutation=%d", w.Code)
	}
	if code, _ := h.admin(http.MethodGet, "/api/runs/missing", nil); code != http.StatusNotFound {
		t.Fatalf("unknown run=%d", code)
	}
}

func TestSignedWebhookIsDurableAndDeduplicated(t *testing.T) {
	h := newHarness(t, 1)
	if code, _ := h.webhook("delivery", func(headers map[string]string) { headers["X-Hub-Signature-256"] = "invalid" }); code != http.StatusUnauthorized {
		t.Fatalf("bad signature=%d", code)
	}
	for range 10 {
		if code, body := h.webhook("delivery", nil); code != http.StatusAccepted {
			t.Fatalf("webhook=%d %s", code, body)
		}
	}
	events, err := h.store.PendingEvents(t.Context())
	if err != nil || len(events) != 1 {
		t.Fatalf("pending events=%+v,%v", events, err)
	}
	h.tick()
	h.tick()
	runs, err := h.store.ListRuns(t.Context())
	if err != nil || len(runs) != 1 || len(runs[0].Jobs) != 1 || h.runtime.created != 1 {
		t.Fatalf("duplicated work: runs=%+v created=%d err=%v", runs, h.runtime.created, err)
	}
	if runs[0].SHA != testSHA || !bytes.Equal(runs[0].Jobs[0].Plan.Event, events[0].Payload) {
		t.Fatal("immutable SHA/event was not preserved")
	}
	h.complete(runs[0].Jobs[0])
	h.tick()
	r, _ := h.store.GetRun(t.Context(), runs[0].ID)
	if r.State != run.Succeeded || len(h.runtime.pods) != 0 || len(h.provider.reports) < 2 {
		t.Fatalf("closed loop incomplete: %+v pods=%d reports=%d", r, len(h.runtime.pods), len(h.provider.reports))
	}
	if code, body := h.admin(http.MethodGet, "/api/jobs/"+r.Jobs[0].ID+"/logs?after=0", nil); code != http.StatusOK || !bytes.Contains(body, []byte("test passed")) {
		t.Fatalf("durable logs=%d %s", code, body)
	}
}

func TestIndependentJobsRespectConcurrencyAndIdentity(t *testing.T) {
	h := newHarness(t, 2)
	const source = `on: push
jobs:
  a:
    runs-on: small
    steps: [{run: echo a}]
  b:
    runs-on: small
    steps: [{run: echo b}]
  c:
    runs-on: small
    steps: [{run: echo c}]
`
	r := h.manual(source)
	h.tick()
	active, err := h.store.Active(t.Context())
	if err != nil || len(active) != 2 || h.runtime.created != 2 {
		t.Fatalf("concurrency: %+v,%v created=%d", active, err, h.runtime.created)
	}
	code, body := h.request(http.MethodPost, "/internal/jobs/"+active[1].ID+"/claim", "", map[string]string{"Authorization": "Bearer token-" + active[0].ID})
	if code != http.StatusUnauthorized {
		t.Fatalf("cross-job token=%d %s", code, body)
	}
	if code, _ := h.runtimeRequest(http.MethodGet, "credentials", active[0], nil); code != http.StatusConflict {
		t.Fatalf("unclaimed credentials=%d", code)
	}
	h.complete(active[0])
	h.tick()
	active, err = h.store.Active(t.Context())
	if err != nil || len(active) != 2 || h.runtime.created != 3 || len(h.runtime.pods) != 2 {
		t.Fatalf("slot not replenished: %+v,%v created=%d pods=%d", active, err, h.runtime.created, len(h.runtime.pods))
	}
	for _, j := range active {
		h.complete(j)
	}
	h.tick()
	r, err = h.store.GetRun(t.Context(), r.ID)
	if err != nil || r.State != run.Succeeded || len(r.Jobs) != 3 {
		t.Fatalf("aggregate completion: %+v,%v", r, err)
	}
}

func TestServerAndSQLiteRestartRetainClaimAndLogs(t *testing.T) {
	h := newHarness(t, 1)
	r := h.manual(testWorkflow)
	h.tick()
	job := r.Jobs[0]
	if code, body := h.runtimeRequest(http.MethodPost, "claim", job, nil); code != http.StatusOK {
		t.Fatalf("claim=%d %s", code, body)
	}
	first := run.Log{Seq: 1, StepID: "test", Data: "before restart\n"}
	if code, _ := h.runtimeRequest(http.MethodPost, "logs", job, first); code != http.StatusOK {
		t.Fatalf("log=%d", code)
	}
	h.http.Close()
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	h.store, err = storage.Open(t.Context(), h.path)
	if err != nil {
		t.Fatal(err)
	}
	h.start()
	h.tick()
	if h.runtime.created != 1 {
		t.Fatalf("restart recreated job: %d", h.runtime.created)
	}
	if code, _ := h.runtimeRequest(http.MethodPost, "claim", job, nil); code != http.StatusOK {
		t.Fatalf("idempotent re-claim=%d", code)
	}
	if code, _ := h.runtimeRequest(http.MethodPost, "logs", job, first); code != http.StatusOK {
		t.Fatalf("idempotent log replay=%d", code)
	}
	if code, body := h.runtimeRequest(http.MethodPost, "result", job, run.Result{State: run.Succeeded, LastLogSeq: 2}); code != http.StatusConflict {
		t.Fatalf("finish before log ACK=%d %s", code, body)
	}
	if code, _ := h.runtimeRequest(http.MethodPost, "logs", job, run.Log{Seq: 2, Data: "after restart\n"}); code != http.StatusOK {
		t.Fatalf("log continuation=%d", code)
	}
	if code, _ := h.runtimeRequest(http.MethodPost, "result", job, run.Result{State: run.Succeeded, LastLogSeq: 2}); code != http.StatusOK {
		t.Fatalf("finish=%d", code)
	}
	h.tick()
	logs, err := h.store.Logs(t.Context(), job.ID, 0)
	if err != nil || len(logs) != 2 {
		t.Fatalf("restored logs: %+v,%v", logs, err)
	}
}

func TestCancellationWaitsForPodStopAndRerunIsNewAttempt(t *testing.T) {
	h := newHarness(t, 1)
	running := h.manual(testWorkflow)
	h.tick()
	if code, _ := h.runtimeRequest(http.MethodPost, "claim", running.Jobs[0], nil); code != http.StatusOK {
		t.Fatalf("claim=%d", code)
	}
	queued := h.manual(testWorkflow)
	for _, id := range []string{running.ID, queued.ID} {
		if code, _ := h.admin(http.MethodPost, "/api/runs/"+id+"/cancel", nil); code != http.StatusOK {
			t.Fatalf("cancel=%d", code)
		}
	}
	q, _ := h.store.GetRun(t.Context(), queued.ID)
	if q.State != run.Cancelled {
		t.Fatalf("queued not cancelled: %+v", q)
	}
	if code, body := h.runtimeRequest(http.MethodGet, "control", running.Jobs[0], nil); code != http.StatusOK || !bytes.Contains(body, []byte(`"cancel_requested":true`)) {
		t.Fatalf("cancel intent=%d %s", code, body)
	}
	h.tick()
	active, _ := h.store.Active(t.Context())
	if len(active) != 1 || len(h.runtime.pods) != 0 {
		t.Fatalf("slot released before observing stop: active=%d pods=%d", len(active), len(h.runtime.pods))
	}
	h.tick()
	r, _ := h.store.GetRun(t.Context(), running.ID)
	if r.State != run.Cancelled {
		t.Fatalf("running terminal=%+v", r)
	}
	code, body := h.admin(http.MethodPost, "/api/runs/"+running.ID+"/rerun", nil)
	var retry run.Run
	if code != http.StatusCreated || json.Unmarshal(body, &retry) != nil || retry.ID == r.ID || retry.Jobs[0].ID == r.Jobs[0].ID || retry.SHA != r.SHA {
		t.Fatalf("rerun did not preserve source/new identity=%d %s", code, body)
	}
	if code, _ := h.admin(http.MethodPost, "/api/runs/"+retry.ID+"/rerun", nil); code != http.StatusConflict {
		t.Fatalf("live rerun=%d", code)
	}
}

func TestCreateResponseLossAndMissingPodDoNotReplay(t *testing.T) {
	h := newHarness(t, 1)
	h.runtime.loseCreateResponse = true
	r := h.manual(testWorkflow)
	if err := h.server.Tick(t.Context()); err == nil {
		t.Fatal("expected simulated create response loss")
	}
	h.tick()
	job, err := h.store.GetJob(t.Context(), r.Jobs[0].ID)
	if err != nil || job.PodUID == "" || h.runtime.created != 1 {
		t.Fatalf("create recovery: %+v,%v created=%d", job, err, h.runtime.created)
	}
	if code, _ := h.runtimeRequest(http.MethodPost, "claim", job, nil); code != http.StatusOK {
		t.Fatalf("claim=%d", code)
	}
	delete(h.runtime.pods, job.ID)
	h.tick()
	h.tick()
	job, err = h.store.GetJob(t.Context(), job.ID)
	if err != nil || job.State != run.Interrupted || h.runtime.created != 1 {
		t.Fatalf("lost claimed Pod replayed: %+v,%v creates=%d", job, err, h.runtime.created)
	}
}

func TestChecksOutboxRetryDoesNotRerunJob(t *testing.T) {
	h := newHarness(t, 1)
	r := h.manual(testWorkflow)
	h.provider.reportFailures = 1
	if err := h.server.Tick(t.Context()); err == nil {
		t.Fatal("expected simulated Checks outage")
	}
	checks, err := h.store.PendingChecks(t.Context())
	if err != nil || len(checks) != 1 {
		t.Fatalf("lost report intent: %+v,%v", checks, err)
	}
	h.tick()
	checks, err = h.store.PendingChecks(t.Context())
	if err != nil || len(checks) != 0 || h.runtime.created != 1 {
		t.Fatalf("retry: %+v,%v creates=%d", checks, err, h.runtime.created)
	}
	h.complete(r.Jobs[0])
	h.tick()
	last := h.provider.reports[len(h.provider.reports)-1]
	if last.RemoteID != 101 || last.Job.State != run.Succeeded || h.runtime.created != 1 {
		t.Fatalf("terminal check not updated: %+v", last)
	}
}

func TestProviderRetryAndCompileDiagnostics(t *testing.T) {
	h := newHarness(t, 1)
	h.provider.workflowFailures = 1
	if code, _ := h.webhook("retry", nil); code != http.StatusAccepted {
		t.Fatalf("webhook=%d", code)
	}
	if err := h.server.Tick(t.Context()); err == nil {
		t.Fatal("expected temporary provider failure")
	}
	if events, err := h.store.PendingEvents(t.Context()); err != nil || len(events) != 1 {
		t.Fatalf("provider error consumed event: %+v,%v", events, err)
	}
	h.provider.files = map[string][]byte{".github/workflows/bad.yml": []byte("on: push\njobs:\n  bad:\n    runs-on: small\n    unsupported: true\n")}
	h.tick()
	code, body := h.admin(http.MethodGet, "/api/events", nil)
	var events []run.Event
	if code != http.StatusOK || json.Unmarshal(body, &events) != nil || len(events) != 1 || events[0].Error == "" || (len(events[0].Payload) != 0 && string(events[0].Payload) != "null") {
		t.Fatalf("diagnostics missing/raw payload exposed=%d %s", code, body)
	}
}

type failingCreateStore struct {
	Store
	failed bool
}

func (s *failingCreateStore) CreateRun(ctx context.Context, r run.Run, p []run.Plan) (bool, error) {
	if !s.failed {
		s.failed = true
		return false, errors.New("transient disk I/O error")
	}
	return s.Store.CreateRun(ctx, r, p)
}

func TestTransientPersistenceErrorKeepsWebhookPending(t *testing.T) {
	h := newHarness(t, 1)
	h.server.store = &failingCreateStore{Store: h.store}
	if code, _ := h.webhook("db-retry", nil); code != http.StatusAccepted {
		t.Fatalf("webhook=%d", code)
	}
	if err := h.server.Tick(t.Context()); err == nil {
		t.Fatal("persistence failure must be returned for retry")
	}
	events, err := h.store.PendingEvents(t.Context())
	if err != nil || len(events) != 1 {
		t.Fatalf("transient database error consumed event: %+v,%v", events, err)
	}
	h.tick()
	runs, err := h.store.ListRuns(t.Context())
	if err != nil || len(runs) != 1 {
		t.Fatalf("retry did not create run: %+v,%v", runs, err)
	}
}

func TestRuntimeCredentialsScopedAndBackupCreated(t *testing.T) {
	h := newHarness(t, 1)
	source := strings.Replace(testWorkflow, "      - id: test", "      - uses: actions/checkout@v4\n      - id: test", 1)
	r := h.manual(source)
	h.tick()
	job := r.Jobs[0]
	if code, _ := h.runtimeRequest(http.MethodPost, "claim", job, nil); code != http.StatusOK {
		t.Fatalf("claim=%d", code)
	}
	code, body := h.runtimeRequest(http.MethodGet, "credentials", job, nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte("repository-scoped-clone-token")) || len(h.provider.tokenRequests) != 1 || h.provider.tokenRequests[0] != "42:me/repo" {
		t.Fatalf("repository-scoped credentials=%d %s requests=%v", code, body, h.provider.tokenRequests)
	}
	code, body = h.admin(http.MethodPost, "/api/backup", nil)
	var info map[string]string
	if code != http.StatusCreated || json.Unmarshal(body, &info) != nil || !strings.HasPrefix(info["file"], "backup-") {
		t.Fatalf("backup=%d %s", code, body)
	}
	fi, err := os.Stat(filepath.Join(h.cfg.DataDir, info["file"]))
	if err != nil || fi.Size() == 0 || fi.Mode().Perm() != 0600 {
		t.Fatalf("backup file: %+v,%v", fi, err)
	}
}
