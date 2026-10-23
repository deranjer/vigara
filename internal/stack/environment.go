package stack

import (
	"bytes"
	"strings"

	"gopkg.in/yaml.v3"
)

// WithEncryptedInput edits the metadata document while preserving YAML comments.
func WithEncryptedInput(source, file string, refs []RecipientRef) (string, error) {
	if !ValidPath(file) || !strings.HasSuffix(file, ".sops") {
		return "", ErrInvalid
	}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(source), &doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "", ErrInvalid
	}
	root := doc.Content[0]
	lookup := func(node *yaml.Node, key string) *yaml.Node {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return node.Content[i+1]
			}
		}
		return nil
	}
	set := func(node *yaml.Node, key string, value *yaml.Node) {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				node.Content[i+1] = value
				return
			}
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	}
	compose := lookup(root, "compose")
	if compose == nil || compose.Kind != yaml.MappingNode {
		return "", ErrInvalid
	}
	inputs := lookup(compose, "interpolationFiles")
	if inputs == nil {
		inputs = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		set(compose, "interpolationFiles", inputs)
	}
	if inputs.Kind != yaml.SequenceNode {
		return "", ErrInvalid
	}
	found := false
	for _, input := range inputs.Content {
		if input.Kind != yaml.MappingNode {
			return "", ErrInvalid
		}
		p := lookup(input, "path")
		if p != nil && p.Value == file {
			set(input, "sensitivity", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "encrypted"})
			set(input, "format", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "sops-binary"})
			found = true
		}
	}
	if !found {
		var n yaml.Node
		if n.Encode(Input{Path: file, Purpose: "stack", Sensitivity: "encrypted", Format: "sops-binary"}) != nil {
			return "", ErrInvalid
		}
		inputs.Content = append(inputs.Content, &n)
	}
	var recipients yaml.Node
	if recipients.Encode(refs) != nil {
		return "", ErrInvalid
	}
	set(root, "secretRecipientRefs", &recipients)
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if enc.Encode(&doc) != nil {
		return "", ErrInvalid
	}
	_ = enc.Close()
	return out.String(), nil
}
