//go:build !linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

func runEngine(context.Context, []string) error { return errors.New("docker engine requires Linux") }

// Container mode only exists in the Linux runner image.
func runExec(context.Context, *json.Encoder, io.Reader) error {
	return errors.New("--exec 只能由 Linux 容器内的执行器使用")
}
