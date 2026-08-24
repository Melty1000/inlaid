package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFindDoesNotExecuteWorkingDirectoryTools(t *testing.T) {
	directory := t.TempDir()
	planted := BundledPath(directory)
	if err := os.MkdirAll(filepath.Dir(planted), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planted, []byte("not ffmpeg"), 0o755); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	_, _ = find(context.Background(), "", func(_ context.Context, candidate string) error {
		equal, compareErr := filepath.Abs(candidate)
		if compareErr != nil {
			return compareErr
		}
		if pathKey(equal) == pathKey(planted) {
			t.Fatalf("working-directory executable was probed: %s", candidate)
		}
		return errors.New("not selected")
	})
}

func TestFindPrefersExplicitFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), executableName())
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := find(context.Background(), path, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(path)
	if got != want {
		t.Fatalf("Find() = %q, want %q", got, want)
	}
}

func TestFindUsesEnvironmentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media-tool-"+executableName())
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INLAID_FFMPEG", path)
	got, err := find(context.Background(), "", func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(path)
	if got != want {
		t.Fatalf("Find() = %q, want %q", got, want)
	}
}

func TestFindSkipsInvalidCandidate(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "broken-"+executableName())
	valid := filepath.Join(t.TempDir(), "working-"+executableName())
	for _, path := range []string{invalid, valid} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("INLAID_FFMPEG", valid)

	got, err := find(context.Background(), invalid, func(_ context.Context, path string) error {
		if filepath.Clean(path) == filepath.Clean(invalid) {
			return errors.New("not FFmpeg")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(valid)
	if got != want {
		t.Fatalf("Find() = %q, want fallback %q", got, want)
	}
}

func TestFindRejectsInvalidCandidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken-"+executableName())
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INLAID_FFMPEG", "")
	_, err := find(context.Background(), path, func(context.Context, string) error { return errors.New("not FFmpeg") })
	if err == nil || !strings.Contains(err.Error(), "could not run") {
		t.Fatalf("Find() error = %v, want invalid executable error", err)
	}
}

func TestFindStopsWhenContextIsCanceled(t *testing.T) {
	path := filepath.Join(t.TempDir(), executableName())
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := find(ctx, path, func(context.Context, string) error {
		t.Fatal("probe ran after cancellation")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Find() error = %v, want context cancellation", err)
	}
}

func TestBundledPathUsesPlatformExecutable(t *testing.T) {
	want := "ffmpeg"
	if runtime.GOOS == "windows" {
		want = "ffmpeg.exe"
	}
	if got := filepath.Base(BundledPath("root")); got != want {
		t.Fatalf("BundledPath() executable = %q, want %q", got, want)
	}
}

func TestPathKeyUsesPlatformCaseRules(t *testing.T) {
	upper := filepath.Join(t.TempDir(), "FFMPEG")
	lower := filepath.Join(filepath.Dir(upper), "ffmpeg")
	equal := pathKey(upper) == pathKey(lower)
	if wantEqual := runtime.GOOS == "windows"; equal != wantEqual {
		t.Fatalf("path key case equality = %t, want %t on %s", equal, wantEqual, runtime.GOOS)
	}
}
