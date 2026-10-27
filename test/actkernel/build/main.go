// Command build prepares a development-only kernel probe in a caller-selected
// temporary directory. No replacement or dependency is added to forgelet.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/shitamachi/forgelet/test/actkernel"
)

func main() {
	candidate := flag.String("candidate", "upstream", "upstream or gitea")
	dir := flag.String("dir", "", "private scratch directory (required)")
	goos := flag.String("os", runtime.GOOS, "worker OS")
	goarch := flag.String("arch", runtime.GOARCH, "worker architecture")
	tests := flag.Bool("test", false, "also run native race behavior tests")
	race := flag.Bool("race", true, "enable race detector for native behavior tests")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "-dir required")
		os.Exit(2)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	*dir = abs
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := actkernel.Prepare(*dir, actkernel.Candidate(*candidate)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := actkernel.Build(ctx, *dir, filepath.Join(*dir, "worker"), *goos, *goarch); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *tests {
		args := []string{"test", "-count=1", "-v", "."}
		if *race {
			args = append(args, "-race")
		}
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = *dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
