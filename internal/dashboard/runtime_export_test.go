package dashboard

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/cellrender"
)

func TestSnapshotIsTheExactDeliveredTerminalFrame(t *testing.T) {
	root := t.TempDir()
	runtime := NewRuntime(DefaultSettings(), filepath.Join(root, "settings.json"), root)
	defer runtime.Close()
	solver, err := cellframe.NewSolver(cellframe.Config{Columns: 4, Rows: 2, Mode: cellframe.ModeDetailed, Buffers: 2})
	if err != nil {
		t.Fatal(err)
	}
	pixels := make([]byte, 8*4*3)
	for i := 0; i < len(pixels); i += 3 {
		pixels[i], pixels[i+1], pixels[i+2] = 240, 80, 130
	}
	frame, err := solver.SolveRGB24(cellframe.RGB24{Pix: pixels, Width: 8, Height: 4, Stride: 24}, cellframe.SourceMeta{})
	if err != nil {
		t.Fatal(err)
	}
	ansi, err := cellrender.ANSI(frame)
	if err != nil {
		frame.Release()
		t.Fatal(err)
	}
	runtime.acceptCanonicalFrame(PreviewUpdate{ANSI: ansi, Columns: 4, Rows: 2}, frame)
	options := RecordOptions{Width: 160, Height: 80}
	runtime.Snapshot(options)

	var saved RuntimeEvent
	deadline := time.After(3 * time.Second)
	for saved.Kind != RuntimeSnapshotSaved {
		select {
		case event := <-runtime.Events():
			if event.Kind == RuntimeSnapshotError {
				t.Fatalf("snapshot error: %v", event.Err)
			}
			if event.Kind == RuntimeSnapshotSaved {
				saved = event
			}
		case <-deadline:
			t.Fatal("snapshot timed out")
		}
	}

	file, err := os.Open(saved.Path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	got := image.NewRGBA(decoded.Bounds())
	draw.Draw(got, got.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	canonical, ok := runtime.currentCanonicalFrame()
	if !ok {
		t.Fatal("canonical frame missing")
	}
	defer canonical.Release()
	want, err := cellrender.CanvasRGBA(canonical, options.Width, options.Height, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Pix, want.Pix) {
		t.Fatal("snapshot was not rasterized from the exact delivered canonical frame")
	}
}
