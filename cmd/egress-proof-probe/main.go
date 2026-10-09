package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RamazanKara/egress-proof/internal/probe"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Getenv("EGRESS_PROOF_DESTINATIONS"), os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, input string, stdout, stderr io.Writer) int {
	var destinations []string
	if err := json.Unmarshal([]byte(input), &destinations); err != nil || len(destinations) == 0 {
		fmt.Fprintln(stderr, "EGRESS_PROOF_DESTINATIONS must be a non-empty JSON array")
		return 2
	}
	// CNI policy installation is asynchronous at pod creation.
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
	}
	observations := make([]probe.Observation, 0, len(destinations))
	for _, destination := range destinations {
		observations = append(observations, probe.Check(ctx, destination))
	}
	if err := json.NewEncoder(stdout).Encode(observations); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}
