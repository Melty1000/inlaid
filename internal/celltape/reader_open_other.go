//go:build !windows

package celltape

import "os"

func openReplayFile(path string, mode int) (*os.File, error) {
	return os.OpenFile(path, mode, 0)
}
