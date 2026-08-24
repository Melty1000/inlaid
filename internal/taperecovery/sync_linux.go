//go:build linux

package taperecovery

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func syncFile(file *os.File) error {
	return file.Sync()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func renameNoReplace(source, destination string) error {
	return unix.Renameat2(
		unix.AT_FDCWD,
		source,
		unix.AT_FDCWD,
		destination,
		unix.RENAME_NOREPLACE,
	)
}
