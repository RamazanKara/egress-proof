package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/RamazanKara/egress-proof/internal/probe"
)

func TestInvalidDestinations(t *testing.T) {
	for _, input := range []string{"", "null", "[]", "{}", `[1]`, `["localhost"] trailing`} {
		t.Run(input, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), input, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, &stdout, &stderr)
			}
		})
	}
}

func TestProbeOutput(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	input, err := json.Marshal([]string{listener.Addr().String(), "invalid/path"})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), string(input), &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	var observations []probe.Observation
	if err := json.Unmarshal(stdout.Bytes(), &observations); err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 || observations[0].Destination != listener.Addr().String() || observations[0].Outcome != "reachable" || observations[1].Outcome != "error" {
		t.Fatalf("unexpected observations: %+v", observations)
	}
}

func TestCancelledProbeAndOutputFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := run(ctx, `["127.0.0.1:443","localhost"]`, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	var observations []probe.Observation
	if err := json.Unmarshal(stdout.Bytes(), &observations); err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 {
		t.Fatalf("incomplete observations: %+v", observations)
	}
	for _, obs := range observations {
		if obs.Outcome != "error" || obs.Error == "" {
			t.Fatalf("cancelled probe produced connectivity evidence: %+v", obs)
		}
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "closed-output"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if code := run(ctx, `["127.0.0.1:443"]`, file, &stderr); code != 2 || stderr.Len() == 0 {
		t.Fatalf("write error lost: exit %d, stderr %q", code, &stderr)
	}
}
