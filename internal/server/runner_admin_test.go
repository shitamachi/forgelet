package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestRunnerPairingPauseAndRotate(t *testing.T) {
	h := externalHarness(t, 2)
	const device = "web-mac"
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		code, data := h.admin(method, "/api/runners"+path, body)
		if code != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, code, want, data)
		}
		return data
	}
	firstToken := pairDevice(t, h, device)

	// A pending code is listed until it is approved or forgotten.
	id, secret := startPairing(t, h, "spare-host")
	if code, _ := collectPairing(t, h, id, secret); code != 202 {
		t.Fatalf("unapproved pairing returned %d", code)
	}
	var pendingViews []pairingView
	if code, data := h.admin("GET", "/api/runner-pairings", nil); code != 200 {
		t.Fatalf("pairings: %d %s", code, data)
	} else if err := json.Unmarshal(data, &pendingViews); err != nil {
		t.Fatal(err)
	}
	if len(pendingViews) != 1 || pendingViews[0].Hostname != "spare-host" || pendingViews[0].Code == "" {
		t.Fatalf("pending pairing not listed: %+v", pendingViews)
	}
	// Re-pairing an offline device replaces its credential: the code proves
	// possession and the machine is not running anything.
	if code, body := h.admin("POST", "/api/runner-pairings/"+id+"/approve", map[string]string{"name": device}); code != 200 {
		t.Fatalf("re-pairing an offline device: %d %s", code, body)
	}
	if _, repaird := collectPairing(t, h, id, secret); !strings.HasPrefix(repaird, "frr_") || repaird == firstToken {
		t.Fatal("re-pairing did not issue a new credential")
	} else {
		firstToken = repaird
	}

	list := func(want string, paused bool) {
		t.Helper()
		data := call("GET", "", nil, 200)
		if strings.Contains(string(data), "token") || strings.Contains(string(data), "digest") {
			t.Fatal("registration list exposed credential fields")
		}
		var items []runnerView
		if err := json.Unmarshal(data, &items); err != nil || len(items) != 2 {
			t.Fatal("runner inventory did not include both devices", err)
		}
		if items[0].Name != macProfile || items[1].Name != device || items[1].Status != want || items[1].Paused != paused {
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
	poll(firstToken, 204)
	list("online", false)
	// An online or busy device is never taken over by another code.
	intruder, _ := startPairing(t, h, "intruder")
	if code, _ := h.admin("POST", "/api/runner-pairings/"+intruder+"/approve", map[string]string{"name": device}); code != 409 {
		t.Fatalf("online device takeover accepted: %d", code)
	}
	if code, _ := h.admin("DELETE", "/api/runner-pairings/"+intruder, nil); code != 204 {
		t.Fatalf("reject failed: %d", code)
	}
	call("PUT", "/"+device+"/pause", map[string]bool{"paused": true}, 200)
	job := h.manual(strings.ReplaceAll(testWorkflow, "runs-on: small", "runs-on: "+run.MacRunnerLabel)).Jobs[0]
	poll(firstToken, 204)
	list("paused", true)

	h.http.Close()
	h.start()
	list("paused", true)
	call("PUT", "/"+device+"/pause", map[string]bool{"paused": false}, 200)
	poll(firstToken, 200)
	list("busy", false)
	call("POST", "/"+device+"/rotate", nil, 409)
	call("PUT", "/"+device+"/pause", map[string]bool{"paused": true}, 200)
	list("busy", true)
	poll(firstToken, 200) // A previously granted attempt is still replayable.
	if code, body := h.externalRequest("/renew", firstToken, runnerRequest{Attempt: attempt, JobID: job.ID}); code != 200 {
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
	var rotated struct{ Token string }
	if err := json.Unmarshal(call("POST", "/"+device+"/rotate", nil, 200), &rotated); err != nil || !strings.HasPrefix(rotated.Token, "frr_") {
		t.Fatal("rotation did not return a credential", err)
	}
	h.http.Close()
	h.start()
	poll(firstToken, 401)
	call("PUT", "/"+device+"/pause", map[string]bool{"paused": false}, 200)
	attempt = "frja_" + strings.Repeat("c", 64)
	poll(rotated.Token, 204)
	list("online", false)
	now := h.server.now()
	h.server.now = func() time.Time { return now.Add(time.Minute) }
	list("offline", false)
}
