package probe

import (
	"strings"
	"testing"
)

func TestExplain(t *testing.T) {
	for _, tt := range []struct {
		name, expected, outcome, problem, verdict, reason string
		stages                                            []Stage
	}{
		{"reachable", "reachable", "reachable", "", "pass", "matching the expectation", nil},
		{"dns-leak", "blocked", "reachable", "", "fail", "despite the blocked expectation", []Stage{{Name: "dns", Success: true}}},
		{"blocked-timeout", "blocked", "unreachable", "i/o timeout", "pass", "cannot prove a policy drop", nil},
		{"reachable-timeout", "reachable", "unreachable", "i/o timeout", "fail", "reachability was required", nil},
		{"refused", "blocked", "error", "connection refused", "error", "inconclusive: connection refused", nil},
		{"dns-error", "reachable", "error", "NXDOMAIN", "error", "inconclusive: NXDOMAIN", nil},
		{"cancelled", "blocked", "error", "context canceled", "error", "inconclusive: context canceled", nil},
		{"missing-result", "reachable", "error", "", "error", "did not establish a conclusive result", nil},
		{"tcp-leak", "blocked", "reachable", "", "fail", "TCP connection succeeded", []Stage{{Name: "tcp", Success: true}}},
		{"tls-error-leak", "blocked", "error", "certificate expired", "fail", "even if TLS or a later address failed", []Stage{{Name: "tcp", Success: true}, {Name: "tls", Error: "certificate expired"}}},
		{"tls-error-reachable", "reachable", "error", "certificate expired", "error", "inconclusive: certificate expired", []Stage{{Name: "tcp", Success: true}, {Name: "tls", Error: "certificate expired"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			obs := Observation{Outcome: tt.outcome, Error: tt.problem, Stages: tt.stages}
			if got := Verdict(tt.expected, obs); got != tt.verdict {
				t.Fatalf("verdict = %s, want %s", got, tt.verdict)
			}
			if got := Explain(tt.expected, obs); !strings.Contains(got, tt.reason) {
				t.Fatalf("explanation %q does not contain %q", got, tt.reason)
			}
		})
	}
}
