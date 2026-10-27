package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/shitamachi/forgelet/internal/actworker"
	"github.com/shitamachi/forgelet/internal/run"
)

func main() {
	// SIGTERM must terminate this subprocess immediately. The supervisor
	// owns cancellation and container cleanup; upstream must not start post code.
	ctx := context.Background()
	if err := actworker.Run(ctx, os.Stdin, os.Stdout); err != nil {
		// Never print upstream errors: they can contain credentials or raw commands.
		_ = json.NewEncoder(os.Stdout).Encode(run.ActMessage{Result: &run.Result{State: run.Failed, Error: "act worker failed before completing its job"}})
		os.Exit(1)
	}
}
