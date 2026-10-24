package main

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

func validateDiskSpaceYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("disk_space must be a mapping")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
			return fmt.Errorf("disk_space.%s must be an integer number of bytes", key.Value)
		}
	}
	return nil
}
