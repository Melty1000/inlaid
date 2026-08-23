//go:build !windows

package taperecovery

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Automatic recovery is Windows-owned. Keeping the file handle open still
// gives non-Windows callers a stable lifetime, while the Windows build supplies
// the mandatory cross-process lock used by the product.
type claimLease struct {
	file *os.File
	once sync.Once
	err  error
}

func acquireClaim(path string) (*claimLease, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &claimLease{file: file}, nil
}

func (l *claimLease) release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}

// The product recovery directory is Windows-owned. On systems without Windows
// mandatory sharing, conservatively omit staging files because an uncooperative
// live writer cannot be proven absent. Published files remain loadable.
func exclusiveProbe(path string, staging bool) (bool, error) {
	if staging {
		return true, nil
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	return false, file.Close()
}

func renameDurable(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}
