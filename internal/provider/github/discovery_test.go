package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDiscoverPaginationAndMetadataScope(t *testing.T) {
	pages := map[string]int{}
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		pages[r.URL.Path]++
		switch r.URL.Path {
		case "/app":
			writeJSON(t, w, map[string]any{"id": 42, "slug": "test-app"})
		case "/app/installations":
			list := []map[string]any{}
			if r.URL.Query().Get("page") == "1" {
				for i := 1; i <= 100; i++ {
					list = append(list, map[string]any{"id": i, "account": map[string]string{"login": "owner", "type": "User"}, "suspended_at": time.Now()})
				}
				list[0]["suspended_at"] = nil
			}
			writeJSON(t, w, list)
		case "/app/installations/1/access_tokens":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			p := body["permissions"].(map[string]any)
			if len(p) != 1 || (p["metadata"] != "read" && p["contents"] != "read") {
				t.Errorf("excess privilege: %v", body)
			}
			writeToken(t, w)
		case "/installation/repositories":
			repos := []map[string]any{}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			n := 100
			if page == 2 {
				n = 1
			}
			for i := 0; i < n; i++ {
				id := (page-1)*100 + i + 1
				repos = append(repos, map[string]any{"id": id, "full_name": fmt.Sprintf("owner/repo%d", id), "default_branch": "feature/main"})
			}
			writeJSON(t, w, map[string]any{"total_count": 101, "repositories": repos})
		case "/repos/owner/repo1/commits":
			if r.URL.Query().Get("sha") != "feature/main" {
				t.Error(r.URL)
			}
			writeJSON(t, w, []map[string]string{{"sha": strings.Repeat("a", 40)}})
		default:
			if strings.HasSuffix(r.URL.Path, "/access_tokens") {
				writeToken(t, w)
			} else {
				t.Errorf("unexpected %s", r.URL)
				http.NotFound(w, r)
			}
		}
	})
	catalog, err := client.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Installations) != 100 || len(catalog.Repositories) != 101 || pages["/app/installations"] != 2 || pages["/installation/repositories"] != 2 {
		t.Fatalf("incomplete catalog: %+v %v", catalog, pages)
	}
	if catalog.InstallURL != "https://github.com/apps/test-app/installations/new" {
		t.Fatal(catalog.InstallURL)
	}
	sha, err := client.DefaultCommit(t.Context(), Repository{Name: "owner/repo1", InstallationID: 1, DefaultBranch: "feature/main"})
	if err != nil || sha != strings.Repeat("a", 40) {
		t.Fatal(sha, err)
	}
}
func TestDiscoverRejectsPartialOrInvalidSnapshots(t *testing.T) {
	for _, mode := range []string{"wrong-app", "bad-slug", "partial", "duplicate", "bad-repo", "bad-installation"} {
		t.Run(mode, func(t *testing.T) {
			client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/app":
					id := 42
					slug := "test"
					if mode == "wrong-app" {
						id = 43
					}
					if mode == "bad-slug" {
						slug = "../evil"
					}
					writeJSON(t, w, map[string]any{"id": id, "slug": slug})
				case "/app/installations":
					id := 1
					if mode == "bad-installation" {
						id = 0
					}
					writeJSON(t, w, []map[string]any{{"id": id, "account": map[string]string{"login": "owner"}}})
				case "/installation/repositories":
					if mode == "partial" {
						http.Error(w, "sensitive response", http.StatusServiceUnavailable)
						return
					}
					name := "owner/repo"
					if mode == "bad-repo" {
						name = "../escape"
					}
					repos := []map[string]any{{"id": 1, "full_name": name}}
					if mode == "duplicate" {
						repos = append(repos, repos[0])
					}
					writeJSON(t, w, map[string]any{"repositories": repos})
				default:
					writeToken(t, w)
				}
			})
			catalog, err := client.Discover(t.Context())
			if err == nil || len(catalog.Repositories) != 0 || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("published invalid snapshot: %+v %v", catalog, err)
			}
		})
	}
}
