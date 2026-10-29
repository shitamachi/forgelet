package server

import (
	"fmt"

	"github.com/shitamachi/forgelet/internal/run"
)

// Selection is explicit administrator configuration, not guessed from shell
// strings. Root workflow selection applies to its expanded reusable child graph.
func (s *Server) selectImageMirrors(plans []run.Plan, repository, path string) error {
	var groups []string
	for _, w := range s.management().Workflows {
		if w.Repository == repository && w.Path == path {
			groups = w.ImageGroups
			break
		}
	}
	if len(groups) == 0 {
		return nil
	} // Existing workflows retain the full catalog.
	for i := range plans {
		p := &plans[i]
		if p.Control != "" || p.Kernel != "act" {
			continue
		}
		profile := s.cfg.Profiles[p.Profile].Act
		if profile == nil {
			return fmt.Errorf("image groups require an act profile")
		}
		images := map[string]string{}
		for _, name := range groups {
			aliases, ok := profile.ImageGroups[name]
			if !ok {
				return fmt.Errorf("image group %q does not exist in profile %q", name, p.Profile)
			}
			for _, alias := range aliases {
				source, ok := profile.ImageMirrors[alias]
				if !ok {
					return fmt.Errorf("image group %q has missing image %q", name, alias)
				}
				images[alias] = source
			}
		}
		runner := s.cfg.Profiles[p.Profile].Image
		if profile.RunnerImage != "" {
			runner = profile.RunnerImage
		}
		if err := run.ValidateImageMirrors(runner, images); err != nil {
			return err
		}
		p.ImageMirrorsSelected = true
		p.ImageMirrors = images
	}
	return nil
}
