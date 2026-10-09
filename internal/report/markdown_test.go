package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RamazanKara/egress-proof/internal/probe"
	"github.com/RamazanKara/egress-proof/internal/spec"
)

func TestMarkdown(t *testing.T) {
	for _, tt := range []struct {
		name, verdict, outcome, reason string
		runErrors                      []string
	}{
		{"pass", "pass", "reachable", "matching the expectation", nil},
		{"fail", "fail", "unreachable", "reachability was required", nil},
		{"error", "error", "error", "inconclusive: lookup failed", nil},
		{"run-error", "", "", "## Run errors\n\n- cleanup forbidden", []string{"cleanup forbidden"}},
		{"no-checks", "", "", "No checks completed.", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := New(spec.Spec{Namespace: "team", PodSelector: "app=api"}, "sha256:abc", "probe:test")
			r.Cluster = Cluster{Context: "test", Server: "https://cluster.example", KubeSystemUID: "cluster-uid", Version: "v1.35.0"}
			r.Errors = tt.runErrors
			if tt.verdict != "" {
				r.Checks = []Check{{Target: "team/client", Expected: "reachable", Verdict: tt.verdict, Observation: probe.Observation{
					Destination: "example.com", Outcome: tt.outcome, Error: "lookup failed",
					Stages: []probe.Stage{{Name: "dns", Address: "example.com", DurationMS: 42, Success: tt.verdict == "pass", Error: "lookup failed"}},
				}}}
			}
			r.Finish()
			data := string(r.Markdown())
			for _, text := range []string{
				"# Egress Proof\n", "Spec hash: sha256:abc", "Namespace: team", "Pod selector: app=api",
				"Cluster context: test", "API server: https://cluster.example", "Cluster UID: cluster-uid",
				"Kubernetes version: v1.35.0", "Probe image: probe:test", r.StartedAt.Format(time.RFC3339Nano), r.FinishedAt.Format(time.RFC3339Nano),
				fmt.Sprintf("**%d passed, %d failed, %d errors**", r.Summary.Passed, r.Summary.Failed, r.Summary.Errors),
				tt.reason, "timeouts cannot distinguish policy drops from outages", "evidence.json",
			} {
				if !strings.Contains(data, text) {
					t.Errorf("Markdown missing %q: %s", text, data)
				}
			}
			if tt.verdict != "" {
				row := fmt.Sprintf("| team/client | example.com | reachable | %s | %s |", tt.outcome, tt.verdict)
				result := "error: lookup failed"
				if tt.verdict == "pass" {
					result = "ok"
				}
				if !strings.Contains(data, row) || !strings.Contains(data, "| dns | example.com | 42 | "+result+" |") {
					t.Fatalf("missing check or stage: %s", data)
				}
			}
			if strings.Contains(data, "## Run errors") != (len(tt.runErrors) > 0) || !strings.HasSuffix(data, "\n") {
				t.Fatalf("unexpected Markdown structure: %s", data)
			}
		})
	}
}

func TestMarkdownEscaping(t *testing.T) {
	for _, tt := range []struct{ raw, want string }{
		{"example.com", "example.com"},
		{"a|b\r\nc\nd\re", "a&#124;b<br>c<br>d<br>e"},
		{"<script>&\"", "&lt;script&gt;&amp;&#34;"},
		{"[link](url) `code` *_~!#\\", "&#91;link&#93;(url) &#96;code&#96; &#42;&#95;&#126;&#33;&#35;&#92;"},
		{"&#124;", "&amp;&#35;124;"},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			if got := markdownText(tt.raw); got != tt.want {
				t.Fatalf("escaped %q, want %q", got, tt.want)
			}
		})
	}
	r := New(spec.Spec{}, "hash", "probe")
	r.Checks = []Check{{Target: "team/client", Expected: "blocked", Verdict: "fail", Observation: probe.Observation{
		Destination: "https://example.com/a|b\n<script>", Outcome: "error", Error: "TLS <certificate> & trust",
		Stages: []probe.Stage{{Name: "tcp", Address: "example.com:443", Success: true}, {Name: "tls", Address: "example.com:443", Error: "TLS | failed\n<script>"}},
	}}}
	r.Errors = []string{"cleanup | failed\n<script>"}
	r.Finish()
	data := string(r.Markdown())
	for _, text := range []string{"a&#124;b<br>&lt;script&gt;", "TLS &#124; failed<br>&lt;script&gt;", "cleanup &#124; failed<br>&lt;script&gt;", "TCP connection succeeded"} {
		if !strings.Contains(data, text) {
			t.Errorf("Markdown lost escaped evidence %q: %s", text, data)
		}
	}
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "|") && strings.Count(line, "|") != 7 {
			t.Errorf("broken table row: %s", line)
		}
	}
}

func TestReportWriteFailures(t *testing.T) {
	for _, name := range []string{"evidence.json", "junit.xml", "summary.md"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			r := New(spec.Spec{}, "hash", "probe")
			r.Finish()
			if err := r.Write(dir); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("lost %s write error: %v", name, err)
			}
		})
	}
}
