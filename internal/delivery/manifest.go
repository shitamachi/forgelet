package delivery

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shitamachi/forgelet/internal/run"
)

// Rewrite changes only the registered images entry. Unknown Kustomize fields
// and comments survive; aliases, duplicate keys and multiple documents do not.
func Rewrite(source []byte, target Target, digest string) ([]byte, string, error) {
	if len(source) > 256<<10 || !run.ValidDigest(digest) {
		return nil, "", fmt.Errorf("invalid manifest size or digest")
	}
	var doc, extra yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(source))
	if err := dec.Decode(&doc); err != nil {
		return nil, "", err
	}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, "", fmt.Errorf("expected one manifest document")
	}
	count := 0
	var validate func(*yaml.Node, int) error
	validate = func(n *yaml.Node, depth int) error {
		count++
		if depth > 32 || count > 10000 || n.Kind == yaml.AliasNode || n.Anchor != "" {
			return fmt.Errorf("unsupported manifest structure")
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || seen[k.Value] {
					return fmt.Errorf("invalid or duplicate manifest key")
				}
				seen[k.Value] = true
			}
		}
		for _, c := range n.Content {
			if err := validate(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := validate(&doc, 0); err != nil {
		return nil, "", err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, "", fmt.Errorf("manifest must be a mapping")
	}
	root := doc.Content[0]
	if field(root, "kind") == nil || field(root, "kind").Value != "Kustomization" {
		return nil, "", fmt.Errorf("target file must be a Kustomization")
	}
	images := field(root, "images")
	if images == nil || images.Kind != yaml.SequenceNode {
		return nil, "", fmt.Errorf("target image mapping is missing")
	}
	var selected *yaml.Node
	for _, entry := range images.Content {
		name := field(entry, "name")
		if name != nil && name.Value == target.ImageName {
			if selected != nil {
				return nil, "", fmt.Errorf("duplicate target image")
			}
			selected = entry
		}
	}
	if selected == nil {
		return nil, "", fmt.Errorf("registered image name is missing")
	}
	previous := ""
	if old := field(selected, "digest"); old != nil {
		previous = old.Value
	}
	if old := field(selected, "newName"); old != nil && old.Value != target.Image {
		return nil, "", fmt.Errorf("manifest image repository differs from registered target")
	}
	if !run.ValidDigest(previous) {
		previous = ""
	}
	setField(selected, "newName", target.Image)
	setField(selected, "digest", digest)
	for i := 0; i < len(selected.Content); i += 2 {
		if selected.Content[i].Value == "newTag" {
			selected.Content = append(selected.Content[:i], selected.Content[i+2:]...)
			break
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, "", err
	}
	if err := enc.Close(); err != nil {
		return nil, "", err
	}
	return out.Bytes(), previous, nil
}
func field(n *yaml.Node, key string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func setField(n *yaml.Node, key, value string) {
	if old := field(n, key); old != nil {
		old.Kind = yaml.ScalarNode
		old.Tag = "!!str"
		old.Value = value
		old.Content = nil
		return
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}
func RevisionSHA(revision string) string {
	_, sha, ok := strings.Cut(revision, "@sha1:")
	if ok && shaPattern.MatchString(sha) {
		return sha
	}
	if shaPattern.MatchString(revision) {
		return revision
	}
	return ""
}
