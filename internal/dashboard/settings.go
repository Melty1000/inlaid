package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxSettingsBytes = 64 << 10

// Settings is the subset of inlaid-settings.json used to seed the dashboard.
// Unknown fields remain forward-compatible with the persisted settings file.
type Settings struct {
	Device string `json:"Device"`
	// DeviceID is the platform camera's stable identity. Device remains the
	// human-readable label and keeps older settings files compatible.
	DeviceID        string `json:"DeviceID"`
	RenderFPS       int    `json:"RenderFPS"`
	CaptureWidth    int    `json:"CaptureWidth"`
	CaptureHeight   int    `json:"CaptureHeight"`
	CaptureFPS      int    `json:"CaptureFPS"`
	Mirror          bool   `json:"Mirror"`
	Symbols         string `json:"Symbols"`
	Framing         string `json:"Framing"`
	ColorLook       string `json:"ColorLook"`
	LookStrength    int    `json:"LookStrength"`
	SaveFormat      string `json:"SaveFormat"`
	ExportQuality   string `json:"ExportQuality"`
	RecordingFPS    int    `json:"RecordingFPS"`
	RecordingWidth  int    `json:"RecordingWidth"`
	RecordingHeight int    `json:"RecordingHeight"`
	GIFfps          int    `json:"GIFfps"`
	GIFwidth        int    `json:"GIFwidth"`
}

// DefaultSettings target native 1080p/30 capture and a matching high-quality
// save. The live terminal grid is derived from the actual preview area.
func DefaultSettings() Settings {
	return Settings{
		RenderFPS:       30,
		CaptureWidth:    1920,
		CaptureHeight:   1080,
		CaptureFPS:      30,
		Symbols:         "quarter",
		Framing:         "fill",
		ColorLook:       "none",
		LookStrength:    100,
		SaveFormat:      "mp4",
		ExportQuality:   "high",
		RecordingFPS:    30,
		RecordingWidth:  1920,
		RecordingHeight: 1080,
		GIFfps:          30,
		GIFwidth:        1080,
	}
}

// SaveSettings updates the dashboard-owned fields without discarding settings
// written by the established launcher or by future versions. The completed
// document is synced to a sibling temporary file before replacing the target.
func SaveSettings(path string, cfg Settings) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("save settings: path is empty")
	}

	document := make(map[string]json.RawMessage)
	existing, err := readSettingsFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(existing, &document); err != nil {
			return fmt.Errorf("save settings: preserve existing fields: %w", err)
		}
		if document == nil {
			document = make(map[string]json.RawMessage)
		}
	case os.IsNotExist(err):
		// A first save creates the settings file with the same atomic path.
	default:
		return fmt.Errorf("save settings: read existing file: %w", err)
	}

	cfg.normalize()
	knownJSON, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("save settings: encode fields: %w", err)
	}
	known := make(map[string]json.RawMessage)
	if err := json.Unmarshal(knownJSON, &known); err != nil {
		return fmt.Errorf("save settings: prepare fields: %w", err)
	}
	for name, value := range known {
		document[name] = value
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("save settings: encode document: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxSettingsBytes {
		return fmt.Errorf("save settings: encoded document exceeds %d KiB", maxSettingsBytes>>10)
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("save settings: create parent directory: %w", err)
	}
	base := filepath.Base(path)
	temp, err := os.CreateTemp(directory, "."+base+".*.tmp")
	if err != nil {
		return fmt.Errorf("save settings: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	keepTemp := false
	defer func() {
		_ = temp.Close()
		if !keepTemp {
			_ = os.Remove(tempPath)
		}
	}()

	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("save settings: inspect existing file: %w", statErr)
	}
	if err := temp.Chmod(mode); err != nil {
		return fmt.Errorf("save settings: set temporary file permissions: %w", err)
	}
	if _, err := temp.Write(encoded); err != nil {
		return fmt.Errorf("save settings: write temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("save settings: sync temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("save settings: close temporary file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("save settings: replace file: %w", err)
	}
	keepTemp = true
	return nil
}

// LoadSettings reads the persisted settings while falling back field-by-field
// to safe values when the file is missing, malformed, or outside valid ranges.
func LoadSettings(path string) (Settings, error) {
	cfg := DefaultSettings()
	b, err := readSettingsFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("settings not found; using safe defaults")
		}
		return cfg, fmt.Errorf("read settings: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return DefaultSettings(), fmt.Errorf("parse settings: %w; using safe defaults", err)
	}
	cfg.normalize()
	return cfg, nil
}

func readSettingsFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSettingsBytes {
		return nil, fmt.Errorf("settings file exceeds %d KiB", maxSettingsBytes>>10)
	}
	return data, nil
}

func (s *Settings) normalize() {
	d := DefaultSettings()
	s.RenderFPS = boundedOr(s.RenderFPS, 1, 60, d.RenderFPS)
	s.CaptureWidth = boundedOr(s.CaptureWidth, 160, 3840, d.CaptureWidth)
	s.CaptureHeight = boundedOr(s.CaptureHeight, 90, 2160, d.CaptureHeight)
	s.CaptureFPS = boundedOr(s.CaptureFPS, 1, 60, d.CaptureFPS)
	s.RecordingFPS = boundedOr(s.RecordingFPS, 1, 60, d.RecordingFPS)
	s.RecordingWidth = boundedOr(s.RecordingWidth, 320, 3840, d.RecordingWidth)
	s.RecordingHeight = boundedOr(s.RecordingHeight, 180, 2160, d.RecordingHeight)
	s.GIFfps = boundedOr(s.GIFfps, 1, 60, d.GIFfps)
	s.GIFwidth = boundedOr(s.GIFwidth, 320, 1920, d.GIFwidth)
	s.Symbols = strings.ToLower(strings.TrimSpace(s.Symbols))
	if s.Symbols != "half" && s.Symbols != "quarter" && s.Symbols != "all" {
		s.Symbols = d.Symbols
	}
	s.Framing = normalizeFraming(s.Framing, d.Framing)
	s.ColorLook = strings.TrimSpace(s.ColorLook)
	if s.ColorLook == "" || len(s.ColorLook) > 128 {
		s.ColorLook = d.ColorLook
	}
	s.LookStrength = boundedOr(s.LookStrength, 0, 100, d.LookStrength)
	s.SaveFormat = normalizeChoice(s.SaveFormat, d.SaveFormat, "mp4", "gif")
	s.ExportQuality = normalizeChoice(s.ExportQuality, d.ExportQuality, "standard", "high")
}

func normalizeFraming(value, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "fill", "fill window", "fill-window":
		return "fill"
	case "whole", "show whole camera", "show-whole-camera":
		return "whole"
	default:
		return fallback
	}
}

func normalizeChoice(value, fallback string, allowed ...string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

func boundedOr(value, low, high, fallback int) int {
	if value < low || value > high {
		return fallback
	}
	return value
}
