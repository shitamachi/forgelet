package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/run/plan"
)

// planStore holds the executor plans keyed by job run id (M0 in-memory;
// PostgreSQL-backed persistence lands with 0011 T6).
type planStore struct {
	mu    sync.Mutex
	plans map[model.JobRunID]*plan.Plan
}

func newPlanStore() *planStore {
	return &planStore{plans: map[model.JobRunID]*plan.Plan{}}
}

// Put validates and stores the plan (digest must compute).
func (s *planStore) Put(p *plan.Plan) error {
	if _, err := p.Digest(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans[p.JobRunID] = p
	return nil
}

// Get returns the plan for a job run.
func (s *planStore) Get(id model.JobRunID) (*plan.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[id]
	if !ok {
		return nil, fmt.Errorf("plan for %s not found", id)
	}
	return p, nil
}

// readDirSorted lists files (not directories) sorted by name.
func readDirSorted(dir string) ([]yamlFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]yamlFile, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		out = append(out, yamlFile{name: name, data: data})
	}
	return out, nil
}
