package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/shitamachi/forgelet/internal/executor"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) > 1 && os.Args[1] == "act-engine" {
		// A successful startup replaces this process with dockerd.
		log.Print(executor.RunActEngine(ctx, os.Args[2:]))
		os.Exit(1)
	}
	options := executor.RemoteOptions{URL: os.Getenv("FORGELET_URL"), JobID: os.Getenv("JOB_ID"), PodUID: os.Getenv("POD_UID"), TokenFile: os.Getenv("TOKEN_FILE"), Workspace: os.Getenv("WORKSPACE")}
	if value := os.Getenv("FORGELET_ACT_CACHE_OBJECT_BYTES"); value != "" {
		bytes, err := strconv.ParseInt(value, 10, 64)
		if err != nil || bytes <= 0 {
			log.Print("invalid FORGELET_ACT_CACHE_OBJECT_BYTES")
			os.Exit(1)
		}
		options.ActCacheObjectBytes = bytes
	}
	if value := os.Getenv("FORGELET_CONTROL_TIMEOUT"); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 || duration > time.Hour {
			log.Print("invalid FORGELET_CONTROL_TIMEOUT")
			os.Exit(1)
		}
		options.RetryTimeout = duration
	}
	if err := executor.RunRemote(ctx, options); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
