package dashboard

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/colorfx"
)

const (
	maxCustomLooks          = 128
	maxLookDirectoryEntries = 512
	maxCustomLookBytes      = int64(64 << 20)
)

type lookCatalog struct {
	names      []string
	transforms map[string]cellframe.ColorTransform
}

func builtInLookCatalog() lookCatalog {
	catalog := lookCatalog{transforms: make(map[string]cellframe.ColorTransform, 4)}
	for _, name := range []string{"NONE", "WARM", "COOL", "MONO"} {
		transform, _ := colorfx.Builtin(name)
		catalog.transforms[strings.ToLower(name)] = transform
	}
	return catalog
}

// loadLookCatalog reads only direct, regular .cube children. A look file is
// declarative numeric data; symlinks, nested paths, and executable shader or
// FFmpeg syntax are never accepted by this feature.
func loadLookCatalog(directory string) (lookCatalog, error) {
	return loadLookCatalogBounded(directory, maxCustomLookBytes)
}

func loadLookCatalogBounded(directory string, retainedByteLimit int64) (lookCatalog, error) {
	catalog := builtInLookCatalog()
	directoryHandle, err := os.Open(directory)
	if errors.Is(err, fs.ErrNotExist) {
		if mkdirErr := os.MkdirAll(directory, 0o755); mkdirErr != nil {
			return catalog, fmt.Errorf("create color looks folder: %w", mkdirErr)
		}
		return catalog, nil
	}
	if err != nil {
		return catalog, fmt.Errorf("read color looks folder: %w", err)
	}
	defer directoryHandle.Close()
	entries, readErr := directoryHandle.ReadDir(maxLookDirectoryEntries + 1)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return catalog, fmt.Errorf("read color looks folder: %w", readErr)
	}
	directoryTruncated := len(entries) > maxLookDirectoryEntries
	if directoryTruncated {
		entries = entries[:maxLookDirectoryEntries]
	}
	sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name()) })
	var skipped []error
	if directoryTruncated {
		skipped = append(skipped, fmt.Errorf("color looks folder has more than %d entries", maxLookDirectoryEntries))
	}
	customCount := 0
	var retainedBytes int64
	for _, entry := range entries {
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".cube") {
			continue
		}
		if customCount >= maxCustomLooks {
			skipped = append(skipped, fmt.Errorf("more than %d .cube files were found", maxCustomLooks))
			break
		}
		info, infoErr := entry.Info()
		if infoErr != nil || entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			skipped = append(skipped, fmt.Errorf("%s is not a direct regular file", entry.Name()))
			continue
		}
		lut, loadErr := colorfx.LoadCube(filepath.Join(directory, entry.Name()))
		if loadErr != nil {
			skipped = append(skipped, fmt.Errorf("%s: %w", entry.Name(), loadErr))
			continue
		}
		lookBytes := lut.RetainedBytes()
		if retainedByteLimit < retainedBytes || lookBytes > retainedByteLimit-retainedBytes {
			skipped = append(skipped, fmt.Errorf("%s exceeds the %d MiB aggregate color-look budget", entry.Name(), retainedByteLimit>>20))
			continue
		}
		name := safeTerminalText(strings.TrimSpace(lut.Title()))
		if name == "" {
			name = safeTerminalText(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		}
		name = shorten(strings.TrimSpace(name), 64)
		if name == "" {
			skipped = append(skipped, fmt.Errorf("%s has no safe display name", entry.Name()))
			continue
		}
		base := name
		for suffix := 2; catalog.transforms[strings.ToLower(name)] != nil; suffix++ {
			name = fmt.Sprintf("%s (%d)", base, suffix)
		}
		catalog.names = append(catalog.names, name)
		catalog.transforms[strings.ToLower(name)] = lut
		retainedBytes += lookBytes
		customCount++
	}
	return catalog, errors.Join(skipped...)
}

func (catalog lookCatalog) resolve(name string, strength int) (cellframe.ColorTransform, string) {
	key := strings.ToLower(strings.TrimSpace(name))
	transform := catalog.transforms[key]
	if transform == nil {
		key = "none"
		transform = catalog.transforms[key]
	}
	strength = min(max(strength, 0), 100)
	transformID := fmt.Sprintf("%s:%d", key, strength)
	if key == "none" || strength == 0 {
		return nil, transformID
	}
	blended, err := colorfx.WithStrength(transform, float64(strength)/100)
	if err != nil {
		fallback, _ := colorfx.Builtin("none")
		return fallback, "none:100"
	}
	return blended, transformID
}
