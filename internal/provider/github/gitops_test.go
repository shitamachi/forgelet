package github

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shitamachi/forgelet/internal/delivery"
)

func gitopsTarget() delivery.Target {
	return delivery.Target{Name: "app", SourceRepository: "o/source", SourceRef: "refs/heads/main", Repository: "o/gitops", InstallationID: 1, Branch: "main", Path: "apps/kustomization.yaml", Mode: "direct", Image: "ghcr.io/o/app", ImageName: "app", FluxNamespace: "flux-system", Kustomization: "app", Namespace: "apps", Deployment: "app", Container: "app"}
}
func TestGitOpsPreparedCommitAndLostResponseRecovery(t *testing.T) {
	var mu sync.Mutex
	parent := strings.Repeat("a", 40)
	commit := strings.Repeat("b", 40)
	tree := strings.Repeat("c", 40)
	head := parent
	patches, prs := 0, 0
	branch := ""
	var dates []string
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]any
		if r.Method != "GET" {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
		}
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/access_tokens"):
			permissions := body["permissions"].(map[string]any)
			if permissions["contents"] != "write" || permissions["pull_requests"] != "write" {
				t.Error("incorrect write token scope", permissions)
			}
			if repos := body["repositories"].([]any); len(repos) != 1 || repos[0] != "gitops" {
				t.Error("unscoped target token")
			}
			writeToken(t, w)
		case p == "/repos/o/gitops/git/ref/heads/main":
			writeJSON(t, w, map[string]any{"object": map[string]string{"sha": head}})
		case strings.Contains(p, "/git/ref/heads/forgelet/"):
			if branch == "" {
				w.WriteHeader(404)
			} else {
				writeJSON(t, w, map[string]any{"object": map[string]string{"sha": commit}})
			}
		case strings.Contains(p, "/git/commits/"):
			writeJSON(t, w, map[string]any{"tree": map[string]string{"sha": tree}})
		case strings.Contains(p, "/contents/"):
			if r.URL.Query().Get("ref") != parent {
				t.Error("manifest not fixed to commit")
			}
			data := []byte("kind: Kustomization\nimages: []\n")
			writeJSON(t, w, map[string]any{"path": "apps/kustomization.yaml", "type": "file", "encoding": "base64", "size": len(data), "content": base64.StdEncoding.EncodeToString(data)})
		case p == "/repos/o/gitops/git/trees":
			if body["base_tree"] != tree {
				t.Error("existing files not preserved")
			}
			entries := body["tree"].([]any)
			if len(entries) != 1 || entries[0].(map[string]any)["path"] != "apps/kustomization.yaml" {
				t.Error("unregistered file write")
			}
			writeJSON(t, w, map[string]string{"sha": tree})
		case p == "/repos/o/gitops/git/commits":
			dates = append(dates, body["committer"].(map[string]any)["date"].(string))
			if body["parents"].([]any)[0] != parent {
				t.Error("incorrect parent")
			}
			writeJSON(t, w, map[string]string{"sha": commit})
		case p == "/repos/o/gitops/git/refs/heads/main":
			patches++
			if body["force"] != false {
				t.Error("force push")
			}
			head = commit
			w.WriteHeader(500) // Remote write succeeded, response was lost.
		case p == "/repos/o/gitops/git/refs":
			branch = body["ref"].(string)
			writeJSON(t, w, map[string]any{})
		case p == "/repos/o/gitops/pulls" && r.Method == "POST":
			prs++
			w.WriteHeader(500) // Creation response lost, then list discovers it.
		case p == "/repos/o/gitops/pulls":
			if prs == 0 {
				writeJSON(t, w, []any{})
			} else {
				writeJSON(t, w, []any{map[string]any{"number": 12, "html_url": "https://github.com/o/gitops/pull/12", "head": map[string]string{"sha": commit}}})
			}
		case p == "/repos/o/gitops/pulls/12":
			writeJSON(t, w, map[string]any{"merged": true, "state": "closed", "merge_commit_sha": strings.Repeat("d", 40), "head": map[string]string{"sha": commit}, "base": map[string]string{"ref": "main"}})
		case strings.Contains(p, "/compare/"):
			writeJSON(t, w, map[string]string{"status": "diverged"})
		default:
			t.Errorf("unexpected GitOps request %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	})
	target := gitopsTarget()
	snap, err := client.Snapshot(t.Context(), target, "")
	if err != nil || snap.SHA != parent {
		t.Fatal(snap, err)
	}
	r := delivery.Record{ID: "delivery", Target: target, Parent: parent, CreatedAt: time.Now(), Content: []byte("test"), Digest: "sha256:" + strings.Repeat("a", 64)}
	for i := 0; i < 2; i++ {
		sha, err := client.Prepare(t.Context(), r)
		if err != nil || sha != commit {
			t.Fatal(sha, err)
		}
	}
	if dates[0] != dates[1] {
		t.Fatal("non-deterministic commit metadata")
	}
	r.Commit = commit
	if _, err := client.Publish(t.Context(), r); err == nil {
		t.Fatal("expected simulated lost response")
	}
	pub, err := client.Publish(t.Context(), r)
	if err != nil || pub.Revision != commit || patches != 1 {
		t.Fatal(pub, err, patches)
	}
	mu.Lock()
	head = strings.Repeat("e", 40)
	mu.Unlock()
	if _, err := client.Publish(t.Context(), r); !errors.Is(err, delivery.ErrConflict) {
		t.Fatal(err)
	}
	r.Target.Mode = "pr"
	if _, err := client.Publish(t.Context(), r); err == nil {
		t.Fatal("expected PR response loss")
	}
	pub, err = client.Publish(t.Context(), r)
	if err != nil || pub.PR != 12 || prs != 1 {
		t.Fatal(pub, err, prs)
	}
	r.PR = pub.PR
	pr, err := client.PullRequest(t.Context(), r)
	if err != nil || !pr.Merged || pr.Revision != strings.Repeat("d", 40) {
		t.Fatal(pr, err)
	}
}
