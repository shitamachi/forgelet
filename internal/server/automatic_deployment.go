package server

import (
	"fmt"
	"sort"

	"github.com/shitamachi/forgelet/internal/delivery"
	"github.com/shitamachi/forgelet/internal/run"
	"github.com/shitamachi/forgelet/internal/workflow"
)

func (s *Server) validateAutomaticTarget(t delivery.Target) error {
	if t.OnSuccess == nil {
		return nil
	}
	p, ok := s.cfg.Profiles[t.OnSuccess.Profile]
	if !ok || p.Act != nil {
		return fmt.Errorf("automatic deployment target %q requires an available native profile", t.Name)
	}
	return nil
}

func (s *Server) attachDeployments(plans []run.Plan, e run.Event) ([]run.Plan, error) {
	if e.Name != "push" {
		return plans, nil
	}
	var bindings []workflow.DeploymentBinding
	for _, t := range s.management().Targets {
		if t.OnSuccess == nil || t.SourceRepository != e.Repository || t.SourceRef != e.Ref || t.OnSuccess.WorkflowPath != e.WorkflowPath {
			continue
		}
		if err := s.validateAutomaticTarget(t); err != nil {
			return nil, err
		}
		bindings = append(bindings, workflow.DeploymentBinding{Target: t.Name, Job: t.OnSuccess.Job, Profile: t.OnSuccess.Profile})
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Target < bindings[j].Target })
	return workflow.AttachDeployments(plans, bindings)
}
