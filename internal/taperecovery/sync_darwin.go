//go:build darwin

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
	syncErr := fsyncDescriptor(int(directory.Fd()))
	return errors.Join(syncErr, directory.Close())
}

func fsyncDescriptor(descriptor int) error {
	for {
		err := unix.Fsync(descriptor)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func renameNoReplace(source, destination string) error {
	return unix.RenamexNp(source, destination, unix.RENAME_EXCL)
}
