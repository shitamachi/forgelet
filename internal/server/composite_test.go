package server_test

import (
	"context"
	"testing"

	"github.com/shitamachi/forgelet/internal/provider/github"
)

type fakeActionFetcher struct {
	meta *github.ActionMeta
	err  error
}

func (f *fakeActionFetcher) FetchAction(ctx context.Context, owner, repo, ref, path string) (*github.ActionMeta, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.meta, nil
}

func TestCompositeExpansion(t *testing.T) {
	// Verify ParseUses
	owner, repo, path, ref, err := github.ParseUses("my-org/my-action/sub@v1")
	if err != nil {
		t.Fatalf("ParseUses: %v", err)
	}
	if owner != "my-org" || repo != "my-action" || path != "sub" || ref != "v1" {
		t.Errorf("ParseUses = %q %q %q %q", owner, repo, path, ref)
	}
	// Verify composite action metadata parsing
	fetcher := &fakeActionFetcher{
		meta: &github.ActionMeta{
			RunsUsing: "composite",
			Steps: []github.CompositeActionStep{
				{Name: "inner", Run: "echo hello from composite"},
			},
		},
	}
	meta, err := fetcher.FetchAction(context.Background(), "my-org", "my-action", "v1", "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if meta.RunsUsing != "composite" || len(meta.Steps) != 1 {
		t.Errorf("meta = %+v", meta)
	}
}
