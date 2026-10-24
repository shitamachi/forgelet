package github

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func TestSameRepositoryPullRequestUsesMergeCommit(t *testing.T) {
	body := `{"action":"synchronize","number":12,"repository":{"full_name":"owner/repo"},"installation":{"id":7},"pull_request":{"merge_commit_sha":"` + testSHA + `","head":{"sha":"` + strings.Repeat("b", 40) + `","repo":{"full_name":"owner/repo","fork":false}},"base":{"sha":"` + strings.Repeat("c", 40) + `","ref":"main","repo":{"full_name":"owner/repo"}}}}`
	event, err := ParsePullRequest([]byte(body))
	if err != nil || event.SHA != testSHA || event.SHA == event.HeadSHA || event.Ref != "refs/pull/12/merge" || event.Name != "pull_request" {
		t.Fatal(event, err)
	}
	for _, tc := range []struct {
		body    string
		ignored bool
	}{{strings.Replace(body, `"fork":false`, `"fork":true`, 1), true}, {strings.Replace(body, `"head":{"sha"`, `"head":{"sha"`, 1), false}, {strings.Replace(body, testSHA, "", 1), false}, {strings.Replace(body, `"synchronize"`, `"closed"`, 1), true}} {
		if tc.body == body {
			continue
		}
		_, err := ParsePullRequest([]byte(tc.body))
		if err == nil || tc.ignored && !errors.Is(err, ErrIgnored) {
			t.Error(err)
		}
	}
}

func TestChangedFilesUsesImmutableComparisonAndRejectsTruncation(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		event                run.Event
		count, status        int
		permanent, transient bool
	}{
		{name: "push rename", count: 1},
		{name: "empty diff"},
		{name: "PR head comparison", event: run.Event{Name: "pull_request", HeadSHA: strings.Repeat("d", 40)}, count: 1},
		{name: "below cap", count: 299},
		{name: "truncated", count: 300, permanent: true},
		{name: "new branch", event: run.Event{BeforeSHA: strings.Repeat("0", 40)}, permanent: true},
		{name: "missing base", status: 404, permanent: true},
		{name: "unrelated commits", status: 422, permanent: true},
		{name: "transient API failure", status: 503, transient: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := tc.event
			event.Repository, event.SHA, event.InstallationID = "owner/repo", testSHA, 7
			if event.BeforeSHA == "" {
				event.BeforeSHA = strings.Repeat("b", 40)
			}
			head := event.SHA
			if event.Name == "pull_request" {
				head = event.HeadSHA
			}
			client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/access_tokens") {
					writeToken(t, w)
					return
				}
				if r.URL.Path != "/repos/owner/repo/compare/"+event.BeforeSHA+"..."+head || r.Header.Get("Authorization") != "Bearer installation-secret" {
					t.Error(r.URL, r.Header)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				files := make([]map[string]string, tc.count)
				for i := range files {
					files[i] = map[string]string{"filename": fmt.Sprintf("src/%d.go", i)}
				}
				if len(files) > 0 {
					files[0]["previous_filename"] = "old.go"
				}
				writeJSON(t, w, map[string]any{"files": files})
			})
			files, err := client.ChangedFiles(t.Context(), event)
			if tc.permanent {
				if !errors.Is(err, ErrComparisonUnsupported) {
					t.Fatal(err)
				}
				return
			}
			if tc.transient {
				if err == nil || errors.Is(err, ErrComparisonUnsupported) {
					t.Fatal(err)
				}
				return
			}
			want := tc.count
			if want > 0 {
				want++
				if len(files) < 2 || files[1] != "old.go" {
					t.Fatal(files)
				}
			}
			if err != nil || len(files) != want {
				t.Fatal(files, err)
			}
		})
	}
}
