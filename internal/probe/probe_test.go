package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDestinations(t *testing.T) {
	for _, tt := range []struct {
		raw, host, port, protocol string
	}{
		{"example.com", "example.com", "", "dns"},
		{"service.namespace.svc.cluster.local.", "service.namespace.svc.cluster.local.", "", "dns"},
		{"example.com:8443", "example.com", "8443", "tcp"},
		{"192.0.2.1:80", "192.0.2.1", "80", "tcp"},
		{"[2001:db8::1]:443", "2001:db8::1", "443", "tcp"},
		{"https://example.com/path?q=yes", "example.com", "443", "tls"},
		{"https://[::1]:8443", "::1", "8443", "tls"},
		{"localhost:65535", "localhost", "65535", "tcp"},
		{strings.Repeat("a", 63) + ".com", strings.Repeat("a", 63) + ".com", "", "dns"},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			d, err := ParseDestination(tt.raw)
			if err != nil || d.Host != tt.host || d.Port != tt.port || d.Protocol != tt.protocol {
				t.Fatalf("got %+v, %v", d, err)
			}
		})
	}
	for _, raw := range []string{"", " example.com", "192.0.2.1", "http://example.com", "https://user:secret@example.com", "https://example.com/#fragment", "https://example.com:", "a:0", "a:65536", "a:http", "a:+80", "::1", "bad/name", "bad..name", "-bad", "a;echo", "https:///path", "https://example.com/%zz", "bad-", ".", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 127) + "a"} {
		if _, err := ParseDestination(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestDNS(t *testing.T) {
	obs := Check(context.Background(), "localhost")
	if obs.Outcome != "reachable" || len(obs.ResolvedIPs) == 0 || len(obs.Stages) != 1 || obs.Stages[0].Name != "dns" || !obs.Stages[0].Success {
		t.Fatalf("missing DNS evidence: %+v", obs)
	}
	for _, ip := range obs.ResolvedIPs {
		if net.ParseIP(ip) == nil {
			t.Errorf("invalid resolved IP %q", ip)
		}
	}
}

func TestInvalidCheck(t *testing.T) {
	obs := Check(context.Background(), "invalid/path")
	if obs.Outcome != "error" || obs.Error == "" || len(obs.Stages) != 0 || obs.StartedAt.IsZero() || obs.FinishedAt.Before(obs.StartedAt) {
		t.Fatalf("invalid destination produced connectivity evidence: %+v", obs)
	}
}

func TestTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	obs := Check(context.Background(), listener.Addr().String())
	if obs.Outcome != "reachable" || Verdict("reachable", obs) != "pass" || Verdict("blocked", obs) != "fail" {
		t.Fatalf("reachable socket: %+v", obs)
	}
	if obs.StartedAt.IsZero() || obs.FinishedAt.Before(obs.StartedAt) || len(obs.Stages) != 1 || obs.Stages[0].Name != "tcp" {
		t.Fatalf("missing TCP evidence: %+v", obs)
	}
}

func TestRefusedConnectionIsInconclusive(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	obs := Check(context.Background(), address)
	if obs.Outcome != "error" || obs.Error == "" || Verdict("blocked", obs) != "error" {
		t.Fatalf("refused connection was accepted as a policy block: %+v", obs)
	}
}

func TestTLSCertificateErrorDoesNotProveBlock(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	obs := Check(context.Background(), server.URL)
	if obs.Outcome != "error" || len(obs.Stages) != 2 || !obs.Stages[0].Success || obs.Stages[1].Success || obs.Stages[1].Error == "" {
		t.Fatalf("TLS evidence missing: %+v", obs)
	}
	if Verdict("blocked", obs) != "fail" || Verdict("reachable", obs) != "error" {
		t.Fatal("TLS certificate failure was mistaken for blocked egress")
	}
}

func TestCancellationIsNotBlock(t *testing.T) {
	for _, destination := range []string{"192.0.2.1:443", "localhost", "localhost:443"} {
		t.Run(destination, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			obs := Check(ctx, destination)
			if obs.Outcome != "error" || obs.Error == "" || Verdict("blocked", obs) != "error" || Verdict("reachable", obs) != "error" {
				t.Fatalf("cancellation produced connectivity evidence: %+v", obs)
			}
		})
	}
}

func TestVerdicts(t *testing.T) {
	for _, tt := range []struct {
		expected, observed, verdict string
	}{
		{"reachable", "reachable", "pass"}, {"reachable", "unreachable", "fail"},
		{"blocked", "reachable", "fail"}, {"blocked", "unreachable", "pass"},
		{"reachable", "error", "error"}, {"blocked", "error", "error"},
	} {
		if got := Verdict(tt.expected, Observation{Outcome: tt.observed}); got != tt.verdict {
			t.Errorf("%s/%s = %s, want %s", tt.expected, tt.observed, got, tt.verdict)
		}
	}
}
