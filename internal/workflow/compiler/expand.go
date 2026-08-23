package compiler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shitamachi/forgelet/internal/workflow/syntax"
)

// expandMatrix produces one JobInstance per combination of matrix axes.
// Axes and the combination string are sorted by axis name so instance keys
// are stable across retries (spec 0001 FR-2.5).
func expandMatrix(job *syntax.Job, inst JobInstance) ([]JobInstance, error) {
	if len(job.Matrix) == 0 {
		inst.Matrix = nil
		return []JobInstance{inst}, nil
	}
	axes := make([]string, 0, len(job.Matrix))
	for axis := range job.Matrix {
		values := job.Matrix[axis]
		if len(values) == 0 {
			return nil, fmt.Errorf("compile: job %q matrix axis %q has no values", job.ID, axis)
		}
		axes = append(axes, axis)
	}
	sort.Strings(axes)

	combos := [][]string{nil} // list of "axis=value" parts, in axis order
	for _, axis := range axes {
		var next [][]string
		for _, combo := range combos {
			for _, value := range job.Matrix[axis] {
				next = append(next, append(append([]string(nil), combo...), axis+"="+value))
			}
		}
		combos = next
	}

	out := make([]JobInstance, 0, len(combos))
	for _, combo := range combos {
		instance := inst
		instance.Matrix = map[string]string{}
		parts := make([]string, 0, len(combo))
		for _, part := range combo {
			eq := strings.Index(part, "=")
			instance.Matrix[part[:eq]] = part[eq+1:]
			parts = append(parts, part[eq+1:])
		}
		instance.Key = fmt.Sprintf("%s[%s]", job.ID, strings.Join(parts, ","))
		instance.DisplayName = fmt.Sprintf("%s (%s)", inst.DisplayName, strings.Join(parts, ", "))
		out = append(out, instance)
	}
	return out, nil
}

// topoSort validates needs references and orders jobs so dependencies come
// first. It returns an error on unknown references and cycles.
func topoSort(jobs []*syntax.Job) ([]*syntax.Job, error) {
	byID := make(map[string]*syntax.Job, len(jobs))
	for _, j := range jobs {
		if _, dup := byID[j.ID]; dup {
			return nil, fmt.Errorf("compile: duplicate job %q", j.ID)
		}
		byID[j.ID] = j
	}
	for _, j := range jobs {
		for _, dep := range j.Needs {
			if _, ok := byID[dep]; !ok {
				return nil, fmt.Errorf("compile: job %q needs unknown job %q", j.ID, dep)
			}
		}
	}

	var out []*syntax.Job
	state := map[string]int{} // 0 unvisited, 1 visiting, 2 done
	var visit func(j *syntax.Job) error
	visit = func(j *syntax.Job) error {
		switch state[j.ID] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("compile: needs cycle through job %q", j.ID)
		}
		state[j.ID] = 1
		for _, dep := range j.Needs {
			if err := visit(byID[dep]); err != nil {
				return err
			}
		}
		state[j.ID] = 2
		out = append(out, j)
		return nil
	}
	// Deterministic: visit in document order.
	for _, j := range jobs {
		if err := visit(j); err != nil {
			return nil, err
		}
	}
	return out, nil
}
