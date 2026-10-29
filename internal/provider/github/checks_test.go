package github

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func checkFixture() run.Check {
	return run.Check{Job: run.Job{ID: "job-1", Name: "test", State: run.Queued}, Run: run.Run{Repository: "owner/repo", SHA: testSHA, InstallationID: 7}}
}

func remoteFixture(id, app int64, external string) remoteCheck {
	var result remoteCheck
	result.ID = id
	result.App.ID = app
	result.ExternalID = external
	result.HeadSHA = testSHA
	return result
}

func TestCheckLifecycle(t *testing.T) {
	var mutations []map[string]any
	var requests []string
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			var body struct {
				Permissions  map[string]string `json:"permissions"`
				Repositories []string          `json:"repositories"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Permissions) != 1 || body.Permissions["checks"] != "write" || len(body.Repositories) != 1 || body.Repositories[0] != "repo" {
				t.Errorf("check token scope=%+v", body)
			}
			writeToken(t, w)
			return
		}
		if r.Header.Get("Authorization") != "Bearer installation-secret" {
			t.Error("missing installation auth")
		}
		if strings.Contains(r.URL.Path, "/commits/") {
			if r.URL.Query().Get("filter") != "all" || r.URL.Query().Get("app_id") != "42" {
				t.Errorf("lookup query=%v", r.URL.Query())
			}
			writeJSON(t, w, map[string]any{"check_runs": []remoteCheck{remoteFixture(99, 77, "job-1")}})
			return
		}
		if r.Method == http.MethodGet {
			writeJSON(t, w, remoteFixture(11, 42, "job-1"))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mutations = append(mutations, body)
		writeJSON(t, w, remoteFixture(11, 42, "job-1"))
	})
	check := checkFixture()
	for _, state := range []run.State{run.Queued, run.Running, run.Succeeded} {
		check.Job.State = state
		id, err := client.ReportCheck(t.Context(), check, "https://forgelet.example/runs/r1")
		if err != nil {
			t.Fatal(err)
		}
		if id != 11 {
			t.Fatalf("check ID=%d", id)
		}
		check.RemoteID = id
	}
	if len(mutations) != 3 {
		t.Fatalf("mutations=%v", mutations)
	}
	if mutations[0]["head_sha"] != testSHA || mutations[0]["external_id"] != "job-1" || mutations[0]["status"] != "queued" {
		t.Fatalf("create=%v", mutations[0])
	}
	if mutations[1]["status"] != "in_progress" || mutations[1]["started_at"] == nil {
		t.Fatalf("start=%v", mutations[1])
	}
	if mutations[2]["status"] != "completed" || mutations[2]["conclusion"] != "success" || mutations[2]["completed_at"] == nil {
		t.Fatalf("complete=%v", mutations[2])
	}
	if mutations[2]["details_url"] != "https://forgelet.example/runs/r1" || mutations[2]["head_sha"] != nil {
		t.Fatalf("patch=%v", mutations[2])
	}
	postCount := 0
	for _, request := range requests {
		if request == "POST /repos/owner/repo/check-runs" {
			postCount++
		}
	}
	if postCount != 1 {
		t.Fatalf("POST count=%d requests=%v", postCount, requests)
	}
}

func TestCheckRecoversLostCreateResponseWithoutDuplicate(t *testing.T) {
	var mu sync.Mutex
	created, posts := false, 0
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			writeToken(t, w)
			return
		}
		if strings.Contains(r.URL.Path, "/commits/") {
			checks := []remoteCheck{}
			if created {
				checks = append(checks, remoteFixture(12, 42, "job-1"))
			}
			writeJSON(t, w, map[string]any{"check_runs": checks})
			return
		}
		if r.Method == http.MethodPost {
			posts++
			created = true
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("missing HTTP hijacker")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		writeJSON(t, w, remoteFixture(12, 42, "job-1"))
	})
	check := checkFixture()
	if _, err := client.ReportCheck(t.Context(), check, ""); err == nil {
		t.Fatal("expected lost response error")
	}
	id, err := client.ReportCheck(t.Context(), check, "")
	if err != nil || id != 12 {
		t.Fatalf("retry ID=%d error=%v", id, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Fatalf("duplicate creates: %d", posts)
	}
}

func TestCheckAppAndIdentityIsolation(t *testing.T) {
	for _, existing := range []remoteCheck{remoteFixture(11, 77, "job-1"), remoteFixture(11, 42, "another-job"), remoteFixture(12, 42, "job-1")} {
		t.Run(strconv.FormatInt(existing.App.ID, 10)+existing.ExternalID+strconv.FormatInt(existing.ID, 10), func(t *testing.T) {
			mutated := false
			client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/access_tokens") {
					writeToken(t, w)
					return
				}
				if r.Method != http.MethodGet {
					mutated = true
				}
				writeJSON(t, w, existing)
			})
			check := checkFixture()
			check.RemoteID = 11
			if _, err := client.ReportCheck(t.Context(), check, ""); err == nil {
				t.Fatal("accepted check identity mismatch")
			}
			if mutated {
				t.Fatal("mutated foreign check")
			}
		})
	}
}

func TestCheckPagination(t *testing.T) {
	pages := 0
	posted := false
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			writeToken(t, w)
			return
		}
		if strings.Contains(r.URL.Path, "/commits/") {
			pages++
			checks := []remoteCheck{remoteFixture(12, 42, "job-1")}
			if r.URL.Query().Get("page") == "1" {
				checks = make([]remoteCheck, 100)
				for i := range checks {
					checks[i] = remoteFixture(int64(i+1), 77, "other")
				}
			}
			writeJSON(t, w, map[string]any{"check_runs": checks})
			return
		}
		if r.Method == http.MethodPost {
			posted = true
		}
		writeJSON(t, w, remoteFixture(12, 42, "job-1"))
	})
	if _, err := client.ReportCheck(t.Context(), checkFixture(), ""); err != nil {
		t.Fatal(err)
	}
	if pages != 2 || posted {
		t.Fatalf("pages=%d duplicate POST=%v", pages, posted)
	}
}

func TestCheckStateMappingAndValidation(t *testing.T) {
	for _, tc := range []struct {
		state              run.State
		status, conclusion string
	}{
		{run.Queued, "queued", ""}, {run.Starting, "queued", ""}, {run.Running, "in_progress", ""},
		{run.Succeeded, "completed", "success"}, {run.Failed, "completed", "failure"}, {run.Cancelled, "completed", "cancelled"},
		{run.Skipped, "completed", "skipped"}, {run.TimedOut, "completed", "timed_out"}, {run.Interrupted, "completed", "action_required"},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			status, conclusion, err := checkStatus(tc.state)
			if err != nil || status != tc.status || conclusion != tc.conclusion {
				t.Fatalf("%s %s %v", status, conclusion, err)
			}
		})
	}
	if _, _, err := checkStatus("unknown"); err == nil {
		t.Fatal("accepted unknown state")
	}
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid check reached API") })
	check := checkFixture()
	check.Run.SHA = "main"
	if _, err := client.ReportCheck(t.Context(), check, ""); err == nil {
		t.Fatal("accepted mutable SHA")
	}
	check = checkFixture()
	if _, err := client.ReportCheck(t.Context(), check, "javascript:alert(1)"); err == nil {
		t.Fatal("accepted unsafe details URL")
	}
}

func TestDeletedCheckIsReconciledAndEmptyDetailsAreOmitted(t *testing.T) {
	for _, found := range []bool{false, true} {
		t.Run(strconv.FormatBool(found), func(t *testing.T) {
			client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/access_tokens") {
					writeToken(t, w)
					return
				}
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/check-runs/11") {
					http.NotFound(w, r)
					return
				}
				if strings.Contains(r.URL.Path, "/commits/") {
					checks := []remoteCheck{}
					if found {
						checks = append(checks, remoteFixture(12, 42, "job-1"))
					}
					writeJSON(t, w, map[string]any{"check_runs": checks})
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if _, ok := body["details_url"]; ok {
					t.Fatal("empty details should be omitted")
				}
				if found && r.Method != "PATCH" || !found && r.Method != "POST" {
					t.Fatal(r.Method)
				}
				writeJSON(t, w, remoteFixture(12, 42, "job-1"))
			})
			check := checkFixture()
			check.RemoteID = 11
			id, err := client.ReportCheck(t.Context(), check, "")
			if err != nil || id != 12 {
				t.Fatal(id, err)
			}
		})
	}
}

func TestPRCheckHeadAndLegacyMigration(t *testing.T) {
	for _, mode := range []string{"new", "legacy", "foreign App", "unrelated SHA"} {
		t.Run(mode, func(t *testing.T) {
			head := strings.Repeat("b", 40)
			check := checkFixture()
			check.Job.Plan.EventName = "pull_request"
			check.Job.Plan.Event = json.RawMessage(`{"pull_request":{"head":{"sha":"` + head + `","repo":{"full_name":"owner/repo","fork":false}}}}`)
			if mode != "new" {
				check.RemoteID = 11
			}
			mutations := 0
			retired := false
			client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/access_tokens") {
					writeToken(t, w)
					return
				}
				if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/commits/") {
					if !strings.Contains(r.URL.Path, head) {
						t.Error("searched wrong commit", r.URL.Path)
					}
					writeJSON(t, w, map[string]any{"check_runs": []remoteCheck{}})
					return
				}
				if r.Method == http.MethodGet {
					old := remoteFixture(11, 42, "job-1")
					if mode == "foreign App" {
						old.App.ID = 77
					}
					if mode == "unrelated SHA" {
						old.HeadSHA = strings.Repeat("d", 40)
					}
					writeJSON(t, w, old)
					return
				}
				mutations++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if r.URL.Path == "/repos/owner/repo/check-runs/11" {
					retired = true
					if body["conclusion"] != "neutral" || body["status"] != "completed" {
						t.Error(body)
					}
					writeJSON(t, w, remoteFixture(11, 42, "job-1"))
					return
				}
				if body["head_sha"] != head || body["name"] != "forgelet / PR / test" {
					t.Error(body)
				}
				created := remoteFixture(22, 42, "job-1")
				created.HeadSHA = head
				writeJSON(t, w, created)
			})
			id, err := client.ReportCheck(t.Context(), check, "https://forgelet.example/runs/pr")
			if mode == "foreign App" || mode == "unrelated SHA" {
				if err == nil || mutations != 0 {
					t.Fatal(id, err, mutations)
				}
				return
			}
			if err != nil || id != 22 || retired != (mode == "legacy") {
				t.Fatal(id, err, retired)
			}
			if check.Run.SHA != testSHA {
				t.Fatal("execution SHA changed")
			}
		})
	}
	check := checkFixture()
	check.Job.Plan.EventName = "pull_request"
	check.Job.Plan.Event = json.RawMessage(`{}`)
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("network called for invalid PR identity") })
	if _, err := client.ReportCheck(t.Context(), check, ""); err == nil {
		t.Fatal("accepted missing source head")
	}
}
