package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RamazanKara/egress-proof/internal/probe"
	"github.com/RamazanKara/egress-proof/internal/report"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestUsage(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
	}{
		{nil, 2}, {[]string{"--help"}, 0}, {[]string{"--unknown"}, 2}, {[]string{"--spec", "missing-file"}, 2},
		{[]string{"--spec", "spec.yaml", "--image", ""}, 2},
		{[]string{"--spec", "spec.yaml", "--out", ""}, 2},
		{[]string{"--spec", "spec.yaml", "extra"}, 2},
		{[]string{"--validate"}, 2}, {[]string{"--explain"}, 2},
	} {
		var output bytes.Buffer
		if got := run(context.Background(), tt.args, &output, &output); got != tt.code {
			t.Fatalf("%v: exit %d, want %d", tt.args, got, tt.code)
		}
	}
}

func TestValidateOffline(t *testing.T) {
	for _, tt := range []struct {
		name, data, message string
		code                int
	}{
		{"namespace", "namespace: team\nreachable: [example.com]\n", "1 checks, sha256:", 0},
		{"selector", "podSelector: app=api\nreachable: [example.com]\nblocked: ['192.0.2.1:443']\n", "2 checks, sha256:", 0},
		{"invalid-destination", "namespace: team\nblocked:\n  - http://example.com\n", "line 3: blocked[1]:", 2},
		{"unknown-field", "namespace: team\nreachble: [example.com]\n", "line 2: field reachble", 2},
		{"malformed-yaml", "namespace: team\nreachable: [\n", "parse spec: yaml:", 2},
		{"empty", "", "line 1: spec is empty", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "spec.yaml")
			out := filepath.Join(dir, "reports")
			if err := os.WriteFile(path, []byte(tt.data), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"--spec", path, "--validate", "--explain", "--kubeconfig", filepath.Join(dir, "missing"), "--out", out}, &stdout, &stderr)
			if code != tt.code {
				t.Fatalf("exit %d, want %d: %s", code, tt.code, &stderr)
			}
			if tt.code == 0 {
				if !strings.Contains(stdout.String(), "valid spec: "+path) || !strings.Contains(stdout.String(), tt.message) || stderr.Len() != 0 {
					t.Fatalf("stdout %q, stderr %q", &stdout, &stderr)
				}
			} else if stdout.Len() != 0 || !strings.Contains(stderr.String(), path+": ") || !strings.Contains(stderr.String(), tt.message) {
				t.Fatalf("stdout %q, stderr %q", &stdout, &stderr)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("validation created reports: %v", err)
			}
		})
	}
}

func TestInvalidSpecAndOutput(t *testing.T) {
	for _, tt := range []struct {
		name, spec, message string
	}{
		{"invalid-spec", "namespace: team\nunknown: true", "parse spec"},
		{"output-is-file", "namespace: team\nreachable: [example.com]", "write evidence"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "spec.yaml")
			if err := os.WriteFile(path, []byte(tt.spec), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"--spec", path, "--kubeconfig", filepath.Join(dir, "missing"), "--out", path}, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), tt.message) || stdout.Len() != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, &stdout, &stderr)
			}
		})
	}
}

func TestClusterRun(t *testing.T) {
	for _, tt := range []struct {
		name, outcome string
		code          int
		explain       bool
	}{
		{"success", "reachable", 0, false},
		{"mismatch", "unreachable", 1, false},
		{"inconclusive", "error", 2, false},
		{"version-error", "", 2, false},
		{"explained-success", "reachable", 0, true},
		{"explained-mismatch", "unreachable", 1, true},
		{"explained-error", "error", 2, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var pod *corev1.Pod
			now := time.Now().UTC()
			obs := probe.Observation{Destination: "example.com", StartedAt: now, FinishedAt: now, Outcome: tt.outcome,
				Stages: []probe.Stage{{Name: "dns", Address: "example.com", DurationMS: 42, Success: tt.outcome == "reachable"}}}
			if tt.outcome != "reachable" {
				obs.Error = "lookup failed"
				obs.Stages[0].Error = obs.Error
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				respond := func(v any) { json.NewEncoder(w).Encode(v) }
				switch {
				case req.URL.Path == "/version":
					if tt.name == "version-error" {
						http.Error(w, "version unavailable", http.StatusForbidden)
						return
					}
					respond(map[string]string{"gitVersion": "v1.35.0"})
				case req.URL.Path == "/api/v1/namespaces/kube-system":
					respond(corev1.Namespace{ObjectMeta: metav1.ObjectMeta{UID: "cluster-uid"}})
				case req.Method == "GET" && req.URL.Path == "/api/v1/namespaces/team/pods":
					respond(corev1.PodList{Items: []corev1.Pod{{
						ObjectMeta: metav1.ObjectMeta{Name: "client", Namespace: "team", UID: "source-uid", Labels: map[string]string{"app": "client"}},
						Spec:       corev1.PodSpec{NodeName: "node", ServiceAccountName: "default", DNSPolicy: corev1.DNSClusterFirst},
						Status:     corev1.PodStatus{Phase: corev1.PodRunning},
					}}})
				case req.Method == "POST":
					data, err := io.ReadAll(req.Body)
					if err != nil {
						t.Error(err)
						return
					}
					pod = &corev1.Pod{}
					if _, _, err := scheme.Codecs.UniversalDeserializer().Decode(data, nil, pod); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					pod.UID = "probe-uid"
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "probe", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}}
					respond(pod)
				case strings.HasSuffix(req.URL.Path, "/log"):
					respond([]probe.Observation{obs})
				case req.Method == "DELETE":
					pod = nil
					respond(metav1.Status{Status: "Success"})
				case pod != nil && req.URL.Path == "/api/v1/namespaces/team/pods/"+pod.Name:
					respond(pod)
				default:
					w.WriteHeader(http.StatusNotFound)
					respond(metav1.Status{Reason: metav1.StatusReasonNotFound, Code: 404})
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			config := fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: test\ncontexts:\n- name: test\n  context:\n    cluster: test\n    namespace: team\nclusters:\n- name: test\n  cluster:\n    server: %s\n", server.URL)
			configPath := filepath.Join(dir, "kubeconfig")
			if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KUBECONFIG", configPath)
			specPath := filepath.Join(dir, "spec.yaml")
			if err := os.WriteFile(specPath, []byte("podSelector: app=client\nreachable: [example.com]"), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			args := []string{"--spec", specPath, "--image", "probe:test", "--out", dir}
			if tt.explain {
				args = append(args, "--explain")
			}
			code := run(context.Background(), args, &stdout, &stderr)
			if code != tt.code || !strings.Contains(stdout.String(), "reports: "+dir) {
				t.Fatalf("exit %d, want %d; stdout %q, stderr %q", code, tt.code, &stdout, &stderr)
			}
			if strings.Contains(stdout.String(), "  dns example.com (42 ms):") != tt.explain {
				t.Fatalf("unexpected explanation details: %s", &stdout)
			}
			if tt.explain {
				for _, text := range []string{probe.Explain("reachable", obs), "error: lookup failed"} {
					if text == "error: lookup failed" && tt.outcome == "reachable" {
						continue
					}
					if !strings.Contains(stdout.String(), text) {
						t.Fatalf("explanation missing %q: %s", text, &stdout)
					}
				}
			}
			data, err := os.ReadFile(filepath.Join(dir, "evidence.json"))
			if err != nil {
				t.Fatal(err)
			}
			var r report.Report
			if err := json.Unmarshal(data, &r); err != nil {
				t.Fatal(err)
			}
			if r.ExitCode() != tt.code || r.Spec.Namespace != "team" || r.Cluster.Context != "test" || r.Cluster.Server != server.URL || r.ProbeImage != "probe:test" {
				t.Fatalf("incorrect evidence: %+v", r)
			}
			if tt.name == "version-error" {
				if len(r.Errors) != 1 || !strings.Contains(stderr.String(), "cluster version") {
					t.Fatalf("lost version error: %+v", r)
				}
			} else if len(r.Checks) != 1 || r.Checks[0].Outcome != tt.outcome || !r.Targets[0].CleanedUp || r.Cluster.KubeSystemUID != "cluster-uid" {
				t.Fatalf("lost probe results: %+v", r)
			}
			if _, err := os.Stat(filepath.Join(dir, "junit.xml")); err != nil {
				t.Fatal(err)
			}
			markdown, err := os.ReadFile(filepath.Join(dir, "summary.md"))
			if err != nil || !strings.Contains(string(markdown), fmt.Sprintf("**%d passed, %d failed, %d errors**", r.Summary.Passed, r.Summary.Failed, r.Summary.Errors)) {
				t.Fatalf("missing Markdown summary: %s, %v", markdown, err)
			}
		})
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
