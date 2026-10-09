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

func FuzzProbeInput(f *testing.F) {
	for _, seed := range []string{"", "[]", "null", "[1]", `["localhost","127.0.0.1:443","invalid/path"]`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr bytes.Buffer
		code := run(ctx, input, &stdout, &stderr)
		if code == 2 {
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatal("invalid input did not produce an error")
			}
			return
		}
		var observations []probe.Observation
		if code != 0 || stderr.Len() != 0 || json.Unmarshal(stdout.Bytes(), &observations) != nil || len(observations) == 0 {
			t.Fatalf("invalid probe output: exit %d, %q, %q", code, &stdout, &stderr)
		}
		var destinations []string
		if err := json.Unmarshal([]byte(input), &destinations); err != nil || len(destinations) != len(observations) {
			t.Fatalf("accepted invalid input: %v", err)
		}
		for i, obs := range observations {
			if obs.Destination != destinations[i] || obs.Outcome != "error" || obs.Error == "" {
				t.Fatalf("cancelled probe produced connectivity evidence: %+v", obs)
			}
		}
	})
}
