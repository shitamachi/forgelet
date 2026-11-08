package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestRunnerRegistrationPauseAndRotate(t *testing.T) {
	h := externalHarness(t, 2)
	const profile = "web-mac"
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		code, data := h.admin(method, "/api/runners"+path, body)
		if code != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, code, want, data)
		}
		return data
	}
	credential := func(data []byte) string {
		t.Helper()
		var reply struct{ Token string }
		if err := json.Unmarshal(data, &reply); err != nil || !strings.HasPrefix(reply.Token, "frr_") || len(reply.Token) != 68 {
			t.Fatal("missing device credential", err)
		}
		return reply.Token
	}
	registration := map[string]string{"name": profile}
	first := credential(call("POST", "", registration, 201))
	call("POST", "", registration, 409)
	list := func(want string, paused bool) {
		t.Helper()
		data := call("GET", "", nil, 200)
		if strings.Contains(string(data), "token") || strings.Contains(string(data), "digest") {
			t.Fatal("registration list exposed credential fields")
		}
		var items []runnerView
		if err := json.Unmarshal(data, &items); err != nil || len(items) != 2 {
			t.Fatal("runner inventory did not include both sources", err)
		}
		if items[0].Name != macProfile || items[0].Source != "yaml" || items[1].Name != profile ||
			items[1].Source != "web" || items[1].Status != want || items[1].Paused != paused {
			t.Fatalf("unexpected inventory: %s", data)
		}
	}
	list("offline", false)
	attempt := "frja_" + strings.Repeat("b", 64)
	poll := func(token string, want int) []byte {
		t.Helper()
		code, data := h.externalRequest("/poll", token, runnerRequest{Attempt: attempt})
		if code != want {
			t.Fatalf("poll: %d, want %d: %s", code, want, data)
		}
		return data
	}
	poll(first, 204)
	list("online", false)
	call("PUT", "/"+profile+"/pause", map[string]bool{"paused": true}, 200)
	job := h.manual(strings.ReplaceAll(testWorkflow, "runs-on: small", "runs-on: "+run.MacRunnerLabel)).Jobs[0]
	poll(first, 204)
	list("paused", true)

	h.http.Close()
	h.start()
	list("paused", true)
	call("PUT", "/"+profile+"/pause", map[string]bool{"paused": false}, 200)
	poll(first, 200)
	list("busy", false)
	call("POST", "/"+profile+"/rotate", nil, 409)
	call("PUT", "/"+profile+"/pause", map[string]bool{"paused": true}, 200)
	list("busy", true)
	poll(first, 200) // A previously granted attempt is still replayable.
	if code, body := h.externalRequest("/renew", first, runnerRequest{Attempt: attempt, JobID: job.ID}); code != 200 {
		t.Fatal(code, string(body))
	}
	for _, step := range []struct {
		path string
		body any
	}{{"claim", nil}, {"result", run.Result{State: run.Succeeded}}} {
		if code, body := h.externalRequest("/jobs/"+job.ID+"/"+step.path, attempt, step.body); code != 200 {
			t.Fatal(code, string(body))
		}
	}
	second := credential(call("POST", "/"+profile+"/rotate", nil, 200))
	h.http.Close()
	h.start()
	poll(first, 401)
	call("PUT", "/"+profile+"/pause", map[string]bool{"paused": false}, 200)
	attempt = "frja_" + strings.Repeat("c", 64)
	poll(second, 204)
	list("online", false)
	now := h.server.now()
	h.server.now = func() time.Time { return now.Add(time.Minute) }
	list("offline", false)
}
