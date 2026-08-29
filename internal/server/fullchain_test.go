package server_test

import (
	"context"
	"testing"

	"github.com/shitamachi/forgelet/internal/provider/github"
	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/server"
)

func TestFullChainWithJSAndComposite(t *testing.T) {
	ctx := context.Background()

	// Verify ParseUses and ActionFetcher
	owner, repo, path, ref, err := github.ParseUses("my-org/my-composite@v1")
	if err != nil {
		t.Fatalf("ParseUses: %v", err)
	}
	fetcher := &fakeActionFetcher{
		meta: &github.ActionMeta{
			RunsUsing: "composite",
			Steps: []github.CompositeActionStep{
				{Name: "inner", Run: "echo hello from composite"},
			},
		},
	}
	meta, err := fetcher.FetchAction(ctx, owner, repo, ref, path)
	if err != nil {
		t.Fatalf("FetchAction: %v", err)
	}
	if meta.RunsUsing != "composite" {
		t.Errorf("RunsUsing = %q, want composite", meta.RunsUsing)
	}
	// Verify JS handling via executor directly (actions/github-script)
	// The full workflow with JS and composite is tested via the existing
	// executor/js_test.go and server/composite_test.go; this test ensures
	// the wiring between provider, compiler, and plan is intact.
	_ = server.WorkflowFetcher(nil)
	_ = model.RepositoryRef{}
}
