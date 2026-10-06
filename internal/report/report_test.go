package report

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RamazanKara/egress-proof/internal/probe"
	"github.com/RamazanKara/egress-proof/internal/spec"
)

func TestReportsAndExitCodes(t *testing.T) {
	r := New(spec.Spec{Namespace: "team"}, "sha256:abc", "probe:test")
	now := time.Now().UTC()
	r.Checks = []Check{{Target: "team/client", Expected: "blocked", Verdict: "pass", Observation: probe.Observation{
		Destination: "192.0.2.1:443", Outcome: "unreachable", Error: "i/o timeout", StartedAt: now, FinishedAt: now.Add(time.Second),
	}}}
	r.Finish()
	if r.ExitCode() != 0 || r.Summary.Passed != 1 {
		t.Fatal("matching check failed")
	}
	r.Checks[0].Verdict = "fail"
	r.Finish()
	if r.ExitCode() != 1 || r.Summary.Failed != 1 {
		t.Fatal("mismatch passed")
	}
	r.Checks = append(r.Checks, Check{Target: "team/client", Expected: "reachable", Verdict: "error", Observation: probe.Observation{
		Destination: "https://example.com/?x=1&y=2", Outcome: "error", Error: "TLS <certificate> & trust", StartedAt: now, FinishedAt: now,
	}})
	r.Errors = []string{"cleanup forbidden"}
	r.Finish()
	if r.ExitCode() != 2 || r.Summary.Errors != 2 {
		t.Fatal("execution errors passed")
	}
	dir := t.TempDir()
	if err := r.Write(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.SpecHash != r.SpecHash || decoded.Checks[0].Error != "i/o timeout" {
		t.Fatalf("JSON evidence lost: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var suite junitSuite
	if err := xml.Unmarshal(data, &suite); err != nil {
		t.Fatal(err)
	}
	if suite.Tests != 3 || suite.Failures != 1 || suite.Errors != 2 || suite.Cases[0].Failure == nil || suite.Cases[1].Error == nil {
		t.Fatalf("wrong JUnit counts: %+v", suite)
	}
	if !strings.Contains(suite.Cases[1].Error.Message, "<certificate> & trust") {
		t.Fatal("XML error text was not preserved")
	}
	if err := r.Write(filepath.Join(dir, "evidence.json", "bad")); err == nil {
		t.Fatal("output write failure ignored")
	}
}
