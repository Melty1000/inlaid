package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindPrefersExplicitFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ffmpeg.exe")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Find(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(path)
	if got != want {
		t.Fatalf("Find() = %q, want %q", got, want)
	}
}

func TestFindUsesEnvironmentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media tool.exe")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INLAID_FFMPEG", path)
	got, err := Find("")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(path)
	if got != want {
		t.Fatalf("Find() = %q, want %q", got, want)
	}
}
