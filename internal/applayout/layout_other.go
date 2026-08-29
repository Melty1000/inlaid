//go:build !windows

package applayout

import (
	"fmt"
	"strings"
)

type ResolveOptions struct {
	Executable   string
	SourceRoot   string
	ExplicitRoot string
	WorkingDir   string
}

// Resolve preserves the current source-layout behavior on platforms without a
// supported installed package. Explicit roots remain available to tests.
func Resolve(options ResolveOptions) (Layout, error) {
	if root := strings.TrimSpace(options.SourceRoot); root != "" {
		return Local(root, Source)
	}
	if root := strings.TrimSpace(options.ExplicitRoot); root != "" {
		return Local(root, ExplicitTest)
	}
	if root := strings.TrimSpace(options.WorkingDir); root != "" {
		return Local(root, Source)
	}
	return Layout{}, fmt.Errorf("source layout working directory is empty")
}
