//go:build !windows && !darwin && !linux

package dashboard

import (
	"context"
	"fmt"
	"runtime"
)

func openFolder(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("opening a folder is unsupported on %s", runtime.GOOS)
}
