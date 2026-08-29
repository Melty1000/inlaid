package supportreport

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type preparedWriter func(*os.File, []byte) error

func (c *Collector) Save(root string, prepared Prepared) (Saved, error) {
	if c == nil {
		return Saved{}, errors.New("support report collector is nil")
	}
	return savePrepared(filepath.Join(root, reportDirectory), prepared, writePrepared)
}

// SaveDirectory writes to the resolved report directory. It lets the runtime
// keep report placement independent from recordings and settings.
func (c *Collector) SaveDirectory(directory string, prepared Prepared) (Saved, error) {
	if c == nil {
		return Saved{}, errors.New("support report collector is nil")
	}
	return savePrepared(directory, prepared, writePrepared)
}

func savePrepared(directory string, prepared Prepared, write preparedWriter) (Saved, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return Saved{}, errors.New("support report directory is empty")
	}
	if len(prepared.data) == 0 || len(prepared.data) > MaxReportBytes {
		return Saved{}, errors.New("support report is empty or exceeds its size limit")
	}
	if digest := sha256.Sum256(prepared.data); digest != prepared.digest {
		return Saved{}, errors.New("support report no longer matches its reviewed digest")
	}
	if err := validateFieldPolicy(prepared.data); err != nil {
		return Saved{}, err
	}
	if write == nil {
		return Saved{}, errors.New("support report writer is unavailable")
	}

	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Saved{}, fmt.Errorf("create support report directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return Saved{}, fmt.Errorf("inspect support report directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Saved{}, errors.New("support report location is not a direct directory")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return Saved{}, fmt.Errorf("protect support report directory: %w", err)
		}
	}

	temp, err := os.CreateTemp(directory, ".inlaid-support-*.partial")
	if err != nil {
		return Saved{}, fmt.Errorf("create private support report: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(0o600); err != nil {
		return Saved{}, fmt.Errorf("protect private support report: %w", err)
	}
	if err := write(temp, prepared.data); err != nil {
		return Saved{}, fmt.Errorf("write private support report: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return Saved{}, fmt.Errorf("sync private support report: %w", err)
	}
	if err := temp.Close(); err != nil {
		return Saved{}, fmt.Errorf("close private support report: %w", err)
	}

	digestText := hex.EncodeToString(prepared.digest[:])
	name := "inlaid-support-v1-" + prepared.createdAt.UTC().Format("20060102T150405Z") + "-" + digestText[:12] + ".json"
	path := filepath.Join(directory, name)
	if err := publishNoReplace(tempPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Saved{}, fmt.Errorf("%w: %s", ErrReportExists, name)
		}
		return Saved{}, fmt.Errorf("publish support report without overwrite: %w", err)
	}
	if err := os.Remove(tempPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = os.Remove(path)
		return Saved{}, fmt.Errorf("remove private support report staging file: %w", err)
	}
	return Saved{Path: path, Bytes: len(prepared.data), SHA256: digestText}, nil
}

func writePrepared(file *os.File, data []byte) error {
	written, err := file.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
