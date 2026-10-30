package github

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shitamachi/forgelet/internal/delivery"
)

func TestAutomaticDeliveryRejectsOldSourceAndRecoversLostPublishACK(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		current, ack, rollback bool
		wantWrites             int
		stale                  bool
	}{
		{"current", true, false, false, 1, false}, {"old slow build", false, false, false, 0, true},
		{"lost ACK then source advances", false, true, false, 0, false}, {"explicit rollback", false, false, true, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, commit, source := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
			writes, sourceReads := 0, 0
			c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/access_tokens"):
					writeToken(t, w)
				case r.URL.Path == "/repos/o/gitops/git/ref/heads/main":
					sha := parent
					if tc.ack {
						sha = commit
					}
					writeJSON(t, w, map[string]any{"object": map[string]string{"sha": sha}})
				case r.URL.Path == "/repos/o/source/git/ref/heads/main":
					sourceReads++
					sha := source
					if !tc.current {
						sha = strings.Repeat("d", 40)
					}
					writeJSON(t, w, map[string]any{"object": map[string]string{"sha": sha}})
				case r.URL.Path == "/repos/o/gitops/git/refs/heads/main" && r.Method == "PATCH":
					writes++
					writeJSON(t, w, map[string]any{})
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					w.WriteHeader(500)
				}
			})
			target := gitopsTarget()
			target.OnSuccess = &delivery.OnSuccess{WorkflowPath: ".github/workflows/ci.yml", Job: "image", Profile: "native"}
			rec := delivery.Record{Target: target, Parent: parent, Commit: commit, SourceSHA: source, SourceInstallationID: 7}
			if tc.rollback {
				rec.RollbackOf = "previous"
			}
			_, err := c.Publish(t.Context(), rec)
			if errors.Is(err, delivery.ErrSuperseded) != tc.stale || (!tc.stale && err != nil) || writes != tc.wantWrites {
				t.Fatal(err, writes)
			}
			if (tc.ack || tc.rollback) && sourceReads != 0 {
				t.Fatal("recovery/rollback blocked by new source")
			}
		})
	}
}
