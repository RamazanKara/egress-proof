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
	decoder.KnownFields(true)
	if err := decoder.Decode(&s); err != nil {
		return s, hash, fmt.Errorf("parse spec: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return s, hash, fmt.Errorf("spec must contain exactly one YAML document")
	}
	if s.Namespace == "" && s.PodSelector == "" {
		return s, hash, fmt.Errorf("namespace or podSelector is required")
	}
	if s.Namespace != "" {
		if errs := validation.IsDNS1123Label(s.Namespace); len(errs) != 0 {
			return s, hash, fmt.Errorf("invalid namespace: %v", errs)
		}
	}
	if _, err := labels.Parse(s.PodSelector); err != nil {
		return s, hash, fmt.Errorf("invalid podSelector: %w", err)
	}
	if len(s.Reachable)+len(s.Blocked) == 0 {
		return s, hash, fmt.Errorf("at least one reachable or blocked destination is required")
	}
	seen := make(map[string]bool)
	for _, check := range s.Checks() {
		if seen[check.Destination] {
			return s, hash, fmt.Errorf("duplicate destination %q", check.Destination)
		}
		seen[check.Destination] = true
		if _, err := probe.ParseDestination(check.Destination); err != nil {
			return s, hash, err
		}
	}
	return s, hash, nil
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
