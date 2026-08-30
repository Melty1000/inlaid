package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSettingsKeepsLegacyJSONCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inlaid-settings.json")
	legacy := []byte(`{
  "Device": "Legacy Camera",
  "RecordingWidth": 1280,
  "RecordingHeight": 720,
  "GIFfps": 15,
  "FutureSetting": {"keep": true}
}`)
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if cfg.Device != "Legacy Camera" || cfg.RecordingWidth != 1280 || cfg.GIFfps != 15 {
		t.Fatalf("legacy fields were not loaded: %+v", cfg)
	}
	if cfg.Framing != "fill" || cfg.SaveFormat != "mp4" || cfg.ExportQuality != "high" {
		t.Fatalf("new fields did not receive compatible defaults: %+v", cfg)
	}
}

func TestSaveSettingsPreservesUnknownFieldsAndReplacesFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "inlaid-settings.json")
	initial := []byte(`{
  "Device": "Old Camera",
  "UnknownObject": {"name": "preserve me", "enabled": true},
  "UnknownNumber": 73
}`)
	if err := os.WriteFile(path, initial, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultSettings()
	cfg.Device = "New Camera"
	cfg.Framing = "show whole camera"
	cfg.ColorLook = "warm"
	cfg.SaveFormat = "gif"
	cfg.ExportQuality = "standard"
	if err := SaveSettings(path, cfg); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(written, &document); err != nil {
		t.Fatalf("saved document is not JSON: %v", err)
	}
	if string(document["UnknownNumber"]) != "73" {
		t.Fatalf("unknown number was not preserved: %s", document["UnknownNumber"])
	}
	var unknown struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(document["UnknownObject"], &unknown); err != nil {
		t.Fatal(err)
	}
	if unknown.Name != "preserve me" || !unknown.Enabled {
		t.Fatalf("unknown object was not preserved: %+v", unknown)
	}

	reloaded, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings(saved) error = %v", err)
	}
	if reloaded.Device != "New Camera" || reloaded.Framing != "whole" || reloaded.ColorLook != "warm" || reloaded.SaveFormat != "gif" || reloaded.ExportQuality != "standard" {
		t.Fatalf("saved dashboard fields did not round-trip: %+v", reloaded)
	}

	temps, err := filepath.Glob(filepath.Join(directory, ".inlaid-settings.json.*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary settings files remain: %v", temps)
	}
}

func TestSaveSettingsPersistsClearedDeviceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inlaid-settings.json")
	initial := []byte(`{"Device":"Old Camera","DeviceID":"old-stable-id","FutureSetting":true}`)
	if err := os.WriteFile(path, initial, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultSettings()
	cfg.Device = ""
	cfg.DeviceID = ""
	if err := SaveSettings(path, cfg); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}

	reloaded, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings(saved) error = %v", err)
	}
	if reloaded.DeviceID != "" {
		t.Fatalf("cleared DeviceID reloaded as %q", reloaded.DeviceID)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(written, &document); err != nil {
		t.Fatal(err)
	}
	if string(document["DeviceID"]) != `""` {
		t.Fatalf("DeviceID was not explicitly persisted empty: %s", document["DeviceID"])
	}
	if string(document["FutureSetting"]) != "true" {
		t.Fatalf("unknown field was not preserved: %s", document["FutureSetting"])
	}
}

func TestSaveSettingsCreatesMissingParentBeforeAtomicSave(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "first-run", "settings")
	path := filepath.Join(parent, "inlaid-settings.json")
	if err := SaveSettings(path, DefaultSettings()); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("settings file was not created: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("settings path is not a regular file: %v", info.Mode())
	}
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("settings parent was not created: %v", err)
	}
	temps, err := filepath.Glob(filepath.Join(parent, ".inlaid-settings.json.*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary settings files remain: %v", temps)
	}
}

func TestSaveSettingsRefusesToOverwriteMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inlaid-settings.json")
	malformed := []byte(`{"Device":`)
	if err := os.WriteFile(path, malformed, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(path, DefaultSettings()); err == nil {
		t.Fatal("SaveSettings() succeeded over malformed existing JSON")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(malformed) {
		t.Fatalf("malformed settings file was changed: %q", after)
	}
}

func TestSettingsFileSizeIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inlaid-settings.json")
	oversized := make([]byte, maxSettingsBytes+1)
	if err := os.WriteFile(path, oversized, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(path); err == nil {
		t.Fatal("LoadSettings accepted an oversized file")
	}
	if err := SaveSettings(path, DefaultSettings()); err == nil {
		t.Fatal("SaveSettings overwrote an oversized file")
	}
}

func TestSaveSettingsRejectsEncodingAmplification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inlaid-settings.json")
	original := []byte(`{"Future":"` + strings.Repeat("<", 12_000) + `"}`)
	if len(original) >= maxSettingsBytes {
		t.Fatal("test fixture must enter through the bounded reader")
	}
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(path, DefaultSettings()); err == nil {
		t.Fatal("SaveSettings accepted an amplified document")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("SaveSettings changed the original after rejecting amplification")
	}
}
