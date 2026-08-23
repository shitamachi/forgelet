package server

import (
	"context"
	"fmt"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/schedule"
	"github.com/shitamachi/forgelet/internal/workflow/syntax"
)

// ScheduledRepo enables the internal cron scheduler for one repository
// (spec 0002 T9): its default branch is scanned for `on.schedule` entries.
type ScheduledRepo struct {
	Owner string
	Name  string
}

// DefaultBrancher is implemented by repository-backed workflow sources that
// can resolve a repository's default branch (the GitHub content adapter).
type DefaultBrancher interface {
	DefaultBranch(ctx context.Context, repo model.RepositoryRef) (string, error)
}

// repoSchedules adapts the workflow source into a schedule.Lister over the
// configured repositories' default branches.
type repoSchedules struct {
	repos []ScheduledRepo
	src   *workflowSource
}

// List implements schedule.Lister. Files that do not parse are skipped here;
// they surface as compile errors when a fire actually ingests.
func (l *repoSchedules) List(ctx context.Context) ([]schedule.ScheduledWorkflow, error) {
	var out []schedule.ScheduledWorkflow
	for _, r := range l.repos {
		repo := model.RepositoryRef{Provider: "github", Owner: r.Owner, Name: r.Name}
		ref := ""
		if db, ok := l.src.fetcher.(DefaultBrancher); ok && db != nil {
			branch, err := db.DefaultBranch(ctx, repo)
			if err != nil {
				return nil, fmt.Errorf("schedule: resolve %s/%s default branch: %w", r.Owner, r.Name, err)
			}
			ref = "refs/heads/" + branch
		}
		files, err := l.src.load(ctx, repo, ref)
		if err != nil {
			return nil, fmt.Errorf("schedule: load %s/%s workflows: %w", r.Owner, r.Name, err)
		}
		for _, f := range files {
			wf, perr := syntax.Parse(f.Name, f.Data)
			if perr != nil {
				continue
			}
			for _, c := range wf.On.Schedule {
				out = append(out, schedule.ScheduledWorkflow{
					Repository: repo,
					FileName:   f.Name,
					Ref:        ref,
					Cron:       c,
				})
			}
		}
	}
	return out, nil
}
