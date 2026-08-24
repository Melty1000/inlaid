//go:build windows

package dashboard

import (
	"context"
	"os/exec"
)

func openFolder(ctx context.Context, path string) error {
	return exec.CommandContext(ctx, "explorer.exe", path).Run()
}
