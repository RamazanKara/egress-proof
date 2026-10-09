package spec

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/RamazanKara/egress-proof/internal/probe"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
)

type Spec struct {
	Namespace   string   `yaml:"namespace" json:"namespace"`
	PodSelector string   `yaml:"podSelector" json:"podSelector,omitempty"`
	Reachable   []string `yaml:"reachable" json:"reachable"`
	Blocked     []string `yaml:"blocked" json:"blocked"`
}

type Check struct {
	Destination string
	Expected    string
}

func Parse(data []byte) (Spec, string, error) {
	sum := sha256.Sum256(data)
	hash := fmt.Sprintf("sha256:%x", sum)
	var s Spec
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if err == io.EOF {
			return s, hash, fmt.Errorf("line 1: spec is empty")
		}
		return s, hash, fmt.Errorf("parse spec: %w", err)
	}
	decoder = yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&s); err != nil {
		return s, hash, fmt.Errorf("parse spec: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return s, hash, fmt.Errorf("parse spec: %w", err)
		}
		return s, hash, fmt.Errorf("line %d: spec must contain exactly one YAML document", extra.Line)
	}
	if s.Namespace == "" && s.PodSelector == "" {
		return s, hash, fmt.Errorf("line %d: namespace or podSelector is required", document.Line)
	}
	if s.Namespace != "" {
		if errs := validation.IsDNS1123Label(s.Namespace); len(errs) != 0 {
			return s, hash, fmt.Errorf("line %d: invalid namespace: %v", fieldNode(&document, "namespace").Line, errs)
		}
	}
	if _, err := labels.Parse(s.PodSelector); err != nil {
		return s, hash, fmt.Errorf("line %d: invalid podSelector: %w", fieldNode(&document, "podSelector").Line, err)
	}
	if len(s.Reachable)+len(s.Blocked) == 0 {
		return s, hash, fmt.Errorf("line %d: at least one reachable or blocked destination is required", document.Line)
	}
	seen := make(map[string]bool)
	for _, group := range []struct {
		name         string
		destinations []string
	}{{"reachable", s.Reachable}, {"blocked", s.Blocked}} {
		if len(group.destinations) == 0 {
			continue
		}
		node := fieldNode(&document, group.name)
		for node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		index := 0
		for i, item := range node.Content {
			value := item
			for value.Kind == yaml.AliasNode {
				value = value.Alias
			}
			// yaml.v3 omits null entries when decoding a string slice.
			if value.ShortTag() == "!!null" {
				continue
			}
			destination := group.destinations[index]
			index++
			line := item.Line
			if seen[destination] {
				return s, hash, fmt.Errorf("line %d: %s[%d]: duplicate destination %q", line, group.name, i+1, destination)
			}
			seen[destination] = true
			if _, err := probe.ParseDestination(destination); err != nil {
				return s, hash, fmt.Errorf("line %d: %s[%d]: %w", line, group.name, i+1, err)
			}
		}
	}
	return s, hash, nil
}

func fieldNode(root *yaml.Node, name string) *yaml.Node {
	stack := []*yaml.Node{root}
	seen := make(map[*yaml.Node]bool)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[node] {
			continue
		}
		seen[node] = true
		switch node.Kind {
		case yaml.AliasNode:
			stack = append(stack, node.Alias)
		case yaml.DocumentNode, yaml.SequenceNode:
			for i := len(node.Content) - 1; i >= 0; i-- {
				stack = append(stack, node.Content[i])
			}
		case yaml.MappingNode:
			for i := 0; i < len(node.Content); i += 2 {
				key := node.Content[i]
				for key.Kind == yaml.AliasNode {
					key = key.Alias
				}
				if key.Value == name {
					return node.Content[i+1]
				}
			}
			// Explicit fields override merges; the first merged mapping wins.
			for i := len(node.Content) - 2; i >= 0; i -= 2 {
				if node.Content[i].Tag == "!!merge" {
					stack = append(stack, node.Content[i+1])
				}
			}
		}
	}
	return root
}

func (s Spec) Checks() []Check {
	checks := make([]Check, 0, len(s.Reachable)+len(s.Blocked))
	for _, destination := range s.Reachable {
		checks = append(checks, Check{Destination: destination, Expected: "reachable"})
	}
	for _, destination := range s.Blocked {
		checks = append(checks, Check{Destination: destination, Expected: "blocked"})
	}
	return checks
}
