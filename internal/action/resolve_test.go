package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shitamachi/forgelet/internal/run"
)

func refPlans(uses ...string) []run.Plan {
	plans := []run.Plan{{Kernel: "act"}}
	for _, use := range uses {
		a, _ := Reference(use)
		plans[0].Steps = append(plans[0].Steps, run.Step{Action: a})
	}
	return plans
}
func refObject(w http.ResponseWriter, kind, sha string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"type": kind, "sha": sha}})
}
func TestResolveActionTagsBranchesAnnotatedTagsAndSubpaths(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, kind := range []string{"tag", "branch", "annotated", "sha", "subpath"} {
		t.Run(kind, func(t *testing.T) {
			var paths []string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.URL.User != nil {
					t.Error("public source received a credential")
				}
				paths = append(paths, r.URL.RequestURI())
				switch {
				case strings.Contains(r.URL.Path, "/contents/"):
					if r.URL.Query().Get("ref") != a {
						t.Error("metadata was not SHA fixed", r.URL.String())
					}
					_, _ = fmt.Fprint(w, "runs: {using: node24, main: main.js}\n")
				case strings.Contains(r.URL.Path, "/git/tags/"):
					refObject(w, "commit", a)
				case strings.Contains(r.URL.Path, "/heads/"):
					refObject(w, "commit", a)
				case kind == "branch":
					http.NotFound(w, r)
				case kind == "annotated":
					refObject(w, "tag", b)
				default:
					refObject(w, "commit", a)
				}
			}))
			defer api.Close()
			uses := "owner/repo@release/v1"
			if kind == "sha" {
				uses = "owner/repo@" + strings.ToUpper(a)
			}
			if kind == "subpath" {
				uses = "owner/repo/nested@v1"
			}
			plans := refPlans(uses, uses)
			if err := (Resolver{Client: api.Client(), APIURL: api.URL}).Resolve(t.Context(), plans); err != nil {
				t.Fatal(err)
			}
			if plans[0].Steps[0].Action.SHA != a || len(plans[0].ActionPins) != 1 {
				t.Fatal(plans)
			}
			want := 2
			if kind == "sha" {
				want = 1
			}
			if kind == "branch" || kind == "annotated" {
				want = 3
			}
			if len(paths) != want {
				t.Fatal("duplicate refs were not deduplicated", paths)
			}
			if kind == "subpath" && !strings.Contains(paths[len(paths)-1], "/contents/nested/action.yml") {
				t.Fatal(paths)
			}
		})
	}
}
func TestResolveCompositePinsTransitiveSourcesAndDetectsCycles(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(fmt.Sprint(cycle), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/git/ref/") {
					refObject(w, "commit", strings.Repeat("a", 40))
					return
				}
				if strings.Contains(r.URL.Path, "/owner/root/") {
					_, _ = fmt.Fprint(w, "runs:\n  using: composite\n  steps:\n    - uses: child/repo/path@stable\n    - uses: child/repo/other@stable\n")
					return
				}
				if cycle {
					_, _ = fmt.Fprint(w, "runs:\n  using: composite\n  steps:\n    - uses: owner/root@v1\n")
					return
				}
				_, _ = fmt.Fprint(w, "runs: {using: node20, main: main.js}\n")
			}))
			defer api.Close()
			plans := refPlans("owner/root@v1")
			err := (Resolver{Client: api.Client(), APIURL: api.URL}).Resolve(t.Context(), plans)
			if cycle {
				if err == nil || !strings.Contains(err.Error(), "cyclic") {
					t.Fatal(err)
				}
				return
			}
			if err != nil || len(plans[0].ActionPins) != 2 || plans[0].ActionPins["child/repo@stable"] != strings.Repeat("a", 40) {
				t.Fatal(plans, err)
			}
		})
	}
}
func TestResolveFailsClosedAndClassifiesRetries(t *testing.T) {
	for _, kind := range []string{"missing", "rate", "outage", "bad sha", "tree", "oversized", "invalid metadata", "runtime", "redirect", "tag cycle", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "missing":
					http.NotFound(w, r)
					return
				case "rate":
					w.WriteHeader(429)
					return
				case "outage":
					w.WriteHeader(503)
					return
				case "bad sha":
					refObject(w, "commit", "abc")
					return
				case "tree":
					refObject(w, "tree", strings.Repeat("a", 40))
					return
				case "oversized":
					_, _ = fmt.Fprint(w, strings.Repeat("x", (256<<10)+1))
					return
				case "redirect":
					w.Header().Set("Location", "http://unexpected.invalid/private")
					w.WriteHeader(302)
					return
				case "tag cycle":
					refObject(w, "tag", strings.Repeat("a", 40))
					return
				}
				if strings.Contains(r.URL.Path, "/git/ref/") {
					refObject(w, "commit", strings.Repeat("a", 40))
					return
				}
				if kind == "runtime" {
					_, _ = fmt.Fprint(w, "runs: {using: node12}\n")
					return
				}
				_, _ = fmt.Fprint(w, "runs: [invalid\n")
			}))
			defer api.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			err := (Resolver{Client: api.Client(), APIURL: api.URL}).Resolve(ctx, refPlans("owner/repo@v1"))
			if err == nil {
				t.Fatal("accepted incomplete source lock")
			}
			retry := kind == "rate" || kind == "outage" || kind == "cancelled"
			if errors.Is(err, ErrUnavailable) != retry {
				t.Fatal("wrong retry classification", err)
			}
		})
	}
}
func TestEachNewResolutionRefreshesTagButOwnsPlanLock(t *testing.T) {
	var sequence atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/") {
			_, _ = fmt.Fprint(w, "runs: {using: node20, main: index.js}\n")
			return
		}
		refObject(w, "commit", strings.Repeat(fmt.Sprint(sequence.Add(1)), 40))
	}))
	defer api.Close()
	resolver := Resolver{Client: api.Client(), APIURL: api.URL}
	first, second := refPlans("owner/repo@main"), refPlans("owner/repo@main")
	if err := resolver.Resolve(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := resolver.Resolve(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if first[0].ActionPins["owner/repo@main"] != strings.Repeat("1", 40) || second[0].ActionPins["owner/repo@main"] != strings.Repeat("2", 40) {
		t.Fatal(first, second)
	}
}

func TestMetadataFallbackKeepsSpecialSubpathsAndSHAInRequest(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/ref/") {
			refObject(w, "commit", strings.Repeat("a", 40))
			return
		}
		if r.URL.Query().Get("ref") != strings.Repeat("a", 40) || !strings.Contains(r.URL.EscapedPath(), "dir%23%3F%25/") {
			t.Error("subpath changed fixed query", r.URL.String())
		}
		if strings.HasSuffix(r.URL.Path, "action.yml") {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, "runs: {using: node24, main: index.js}\n")
	}))
	defer api.Close()
	if err := (Resolver{Client: api.Client(), APIURL: api.URL}).Resolve(t.Context(), refPlans("Owner/Repo/dir#?%@v1")); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"https://secret:password@github.com", "https://api.github.com?private=1", "https://api.github.com#secret", "file:///etc/private"} {
		err := (Resolver{APIURL: base}).Resolve(t.Context(), refPlans("owner/repo@v1"))
		if err == nil || strings.Contains(err.Error(), "password") {
			t.Fatal("invalid source base accepted or leaked", err)
		}
	}
}
func TestSourceAndDependencyLimitsAreEnforcedBeforePersistence(t *testing.T) {
	for _, kind := range []string{"sources", "depth"} {
		t.Run(kind, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/git/ref/") {
					refObject(w, "commit", strings.Repeat("a", 40))
					return
				}
				if kind == "sources" {
					_, _ = fmt.Fprint(w, "runs: {using: node20, main: main.js}\n")
					return
				}
				repo := strings.Split(r.URL.Path, "/")[3]
				_, _ = fmt.Fprintf(w, "runs:\n  using: composite\n  steps:\n    - uses: owner/%sx@v1\n", repo)
			}))
			defer api.Close()
			uses := []string{"owner/root@v1"}
			if kind == "sources" {
				for i := 0; i < 65; i++ {
					uses = append(uses, fmt.Sprintf("owner/repo%d@v1", i))
				}
			}
			err := (Resolver{Client: api.Client(), APIURL: api.URL}).Resolve(t.Context(), refPlans(uses...))
			if err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatal(err)
			}
		})
	}
}
