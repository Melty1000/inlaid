// Package ffmpeg locates the optional FFmpeg executable used for MP4 and GIF
// export. Live camera preview does not depend on FFmpeg.
package ffmpeg

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Find resolves FFmpeg from an explicit path, the environment, the release's
// local tools directory, the current checkout, or PATH.
func Find(explicit string) (string, error) {
	choices := make([]string, 0, 8)
	if strings.TrimSpace(explicit) != "" {
		choices = append(choices, explicit)
	}
	if fromEnv := strings.TrimSpace(os.Getenv("INLAID_FFMPEG")); fromEnv != "" {
		choices = append(choices, fromEnv)
	}
	if executable, err := os.Executable(); err == nil {
		root := filepath.Dir(filepath.Dir(executable))
		choices = append(choices, filepath.Join(root, ".tools", "ffmpeg", "bin", "ffmpeg.exe"))
	}
	choices = append(choices, filepath.Join(".tools", "ffmpeg", "bin", "ffmpeg.exe"))
	if fromPath, err := exec.LookPath("ffmpeg"); err == nil {
		choices = append(choices, fromPath)
	}

	seen := make(map[string]struct{}, len(choices))
	for _, choice := range choices {
		clean := filepath.Clean(choice)
		key := strings.ToLower(clean)
		if clean == "." {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		info, err := os.Stat(clean)
		if err != nil || info.IsDir() {
			continue
		}
		if absolute, err := filepath.Abs(clean); err == nil {
			return absolute, nil
		}
		return clean, nil
	}

	return "", errors.New("FFmpeg was not found; restart with START-INLAID.cmd while online, install FFmpeg, or set INLAID_FFMPEG")
}
