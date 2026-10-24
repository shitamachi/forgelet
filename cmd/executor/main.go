package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/shitamachi/forgelet/internal/executor"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	options := executor.RemoteOptions{URL: os.Getenv("FORGELET_URL"), JobID: os.Getenv("JOB_ID"), PodUID: os.Getenv("POD_UID"), TokenFile: os.Getenv("TOKEN_FILE"), Workspace: os.Getenv("WORKSPACE")}
	if err := executor.RunRemote(ctx, options); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
