//go:build linux

package dashboard

import (
	"context"
	"os/exec"
)

func openFolder(ctx context.Context, path string) error {
	return exec.CommandContext(ctx, "xdg-open", path).Run()
}
