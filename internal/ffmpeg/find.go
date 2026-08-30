// Package ffmpeg locates the optional FFmpeg executable used for MP4 and GIF
// export. Live camera preview does not depend on FFmpeg.
package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	findTimeout  = 3 * time.Second
	probeTimeout = time.Second
)

// FindContext resolves and verifies FFmpeg within a bounded caller lifetime.
// localToolsRoot is the active portable, source, or explicit-test root. An
// empty value disables colocated .tools discovery for installed layouts.
func FindContext(parent context.Context, explicit, localToolsRoot string) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, findTimeout)
	defer cancel()
	return find(ctx, explicit, localToolsRoot, probeExecutable)
}

// BundledPath returns the platform-native FFmpeg location under an Inlaid
// project or release root.
func BundledPath(root string) string {
	return filepath.Join(root, ".tools", "ffmpeg", "bin", executableName())
}

func find(ctx context.Context, explicit, localToolsRoot string, probe func(context.Context, string) error) (string, error) {
	choices := make([]string, 0, 8)
	if strings.TrimSpace(explicit) != "" {
		choices = append(choices, explicit)
	}
	if fromEnv := strings.TrimSpace(os.Getenv("INLAID_FFMPEG")); fromEnv != "" {
		choices = append(choices, fromEnv)
	}
	if localToolsRoot = strings.TrimSpace(localToolsRoot); localToolsRoot != "" {
		choices = append(choices, BundledPath(localToolsRoot))
	}
	if fromPath, err := exec.LookPath("ffmpeg"); err == nil {
		choices = append(choices, fromPath)
	}

	seen := make(map[string]struct{}, len(choices))
	var foundInvalid bool
	for _, choice := range choices {
		clean := filepath.Clean(choice)
		if clean == "." {
			continue
		}
		info, err := os.Stat(clean)
		if err != nil || info.IsDir() {
			continue
		}
		candidate := clean
		if absolute, err := filepath.Abs(clean); err == nil {
			candidate = absolute
		}
		key := pathKey(candidate)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if ctx.Err() != nil {
			foundInvalid = true
			break
		}
		if err := probe(ctx, candidate); err != nil {
			foundInvalid = true
			if ctx.Err() != nil {
				break
			}
			continue
		}
		return candidate, nil
	}

	if ctx.Err() != nil {
		return "", fmt.Errorf("FFmpeg discovery: %w", ctx.Err())
	}
	if foundInvalid {
		return "", errors.New("FFmpeg was found but could not run; replace it, install FFmpeg, or set INLAID_FFMPEG")
	}
	return "", errors.New("FFmpeg was not found; install FFmpeg or set INLAID_FFMPEG")
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "ffmpeg.exe"
	}
	return "ffmpeg"
}

func pathKey(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func probeExecutable(parent context.Context, path string) error {
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()

	var stdout, stderr cappedOutput
	command := exec.CommandContext(ctx, path, "-version")
	command.WaitDelay = 500 * time.Millisecond
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("version probe: %w", ctx.Err())
		}
		return fmt.Errorf("version probe: %w", err)
	}
	output := strings.ToLower(stdout.String() + stderr.String())
	if !strings.Contains(output, "ffmpeg version") {
		return errors.New("version probe did not identify FFmpeg")
	}
	return nil
}

type cappedOutput struct{ bytes.Buffer }

func (w *cappedOutput) Write(p []byte) (int, error) {
	const limit = 8 << 10
	remaining := limit - w.Len()
	if remaining > 0 {
		_, _ = w.Buffer.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}
