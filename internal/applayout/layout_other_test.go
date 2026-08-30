//go:build !windows

package applayout

import (
	"path/filepath"
	"testing"
)

func TestResolvePrefersCheckoutRootForExecutableUnderBin(t *testing.T) {
	checkout := t.TempDir()
	workingDirectory := t.TempDir()

	layout, err := Resolve(ResolveOptions{
		Executable: filepath.Join(checkout, "bin", "inlaid"),
		WorkingDir: workingDirectory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if layout.Mode != Source {
		t.Fatalf("mode = %q, want %q", layout.Mode, Source)
	}
	if layout.ProgramRoot != filepath.Clean(checkout) {
		t.Fatalf("program root = %q, want checkout root %q", layout.ProgramRoot, checkout)
	}
}

func TestResolveFallsBackToWorkingDirectoryOutsideBin(t *testing.T) {
	workingDirectory := t.TempDir()

	layout, err := Resolve(ResolveOptions{
		Executable: filepath.Join(t.TempDir(), "inlaid"),
		WorkingDir: workingDirectory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if layout.ProgramRoot != filepath.Clean(workingDirectory) {
		t.Fatalf("program root = %q, want working directory %q", layout.ProgramRoot, workingDirectory)
	}
}

func TestResolveFallsBackToExecutableDirectoryWithoutWorkingDirectory(t *testing.T) {
	executableDirectory := t.TempDir()

	layout, err := Resolve(ResolveOptions{
		Executable: filepath.Join(executableDirectory, "inlaid"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if layout.ProgramRoot != filepath.Clean(executableDirectory) {
		t.Fatalf("program root = %q, want executable directory %q", layout.ProgramRoot, executableDirectory)
	}
}
