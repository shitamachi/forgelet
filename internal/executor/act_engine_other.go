//go:build !linux

package executor

import (
	"context"
	"fmt"
)

func RunActEngine(ctx context.Context, _ []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("act engine requires Linux cgroup v2")
}
