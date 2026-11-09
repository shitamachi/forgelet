//go:build !linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

// Container mode only exists in the Linux runner image.
func runExec(context.Context, *json.Encoder, io.Reader) error {
	return errors.New("--exec 只能由 Linux 容器内的执行器使用")
}
