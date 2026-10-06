package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RamazanKara/egress-proof/internal/probe"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var destinations []string
	if err := json.Unmarshal([]byte(os.Getenv("EGRESS_PROOF_DESTINATIONS")), &destinations); err != nil || len(destinations) == 0 {
		fmt.Fprintln(os.Stderr, "EGRESS_PROOF_DESTINATIONS must be a non-empty JSON array")
		os.Exit(2)
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
	if err := json.NewEncoder(os.Stdout).Encode(observations); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
