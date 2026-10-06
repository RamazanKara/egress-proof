package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/RamazanKara/egress-proof/internal/report"
)

func TestUsage(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
	}{{nil, 2}, {[]string{"--help"}, 0}, {[]string{"--unknown"}, 2}, {[]string{"--spec", "missing-file"}, 2}} {
		var output bytes.Buffer
		if got := run(context.Background(), tt.args, &output, &output); got != tt.code {
			t.Fatalf("%v: exit %d, want %d", tt.args, got, tt.code)
		}
	}
}

func TestConfigurationFailureStillWritesEvidence(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.yaml")
	if err := os.WriteFile(specPath, []byte("namespace: team\nblocked: ['192.0.2.1:443']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := run(context.Background(), []string{"--spec", specPath, "--kubeconfig", filepath.Join(dir, "missing-config"), "--out", dir}, &output, &output)
	if code != 2 {
		t.Fatalf("exit %d: %s", code, output.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r report.Report
	if err := json.Unmarshal(data, &r); err != nil || r.Summary.Errors != 1 || r.SpecHash == "" {
		t.Fatalf("missing error evidence: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "junit.xml")); err != nil {
		t.Fatal(err)
	}
}
