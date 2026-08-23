//go:build windows

package cellreduce_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/cellreduce"
	"github.com/Melty1000/inlaid/internal/mfcapture"
)

// TestRealC922ColorPath is an opt-in end-to-end diagnostic for the native
// planar camera path. It guards against accidentally treating chroma planes as
// neutral or collapsing a truecolor scene into a terminal-wide two-color
// palette.
func TestRealC922ColorPath(t *testing.T) {
	if os.Getenv("INLAID_MF_CAPTURE_REAL") != "1" {
		t.Skip("set INLAID_MF_CAPTURE_REAL=1 to exercise the attached camera")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	devices, err := mfcapture.Enumerate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, device := range devices {
		if strings.Contains(strings.ToLower(device.Name), "c922") {
			id = device.ID
			break
		}
	}
	if id == "" {
		t.Fatalf("C922 not found: %+v", devices)
	}
	cfg := mfcapture.DefaultConfig()
	cfg.DeviceID = id
	session, err := mfcapture.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var frame *mfcapture.Frame
	for frame == nil {
		select {
		case frame = <-session.Frames:
		case err := <-session.Errors:
			t.Logf("transient capture error: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	defer frame.Release()

	minmax := func(p []byte) (uint8, uint8, uint64) {
		lo, hi := uint8(255), uint8(0)
		var sum uint64
		for _, value := range p {
			lo = min(lo, value)
			hi = max(hi, value)
			sum += uint64(value)
		}
		return lo, hi, sum / uint64(len(p))
	}
	yMin, yMax, yMean := minmax(frame.Y.Pix)
	cbMin, cbMax, cbMean := minmax(frame.Cb.Pix)
	crMin, crMax, crMean := minmax(frame.Cr.Pix)
	t.Logf("planes Y=%d..%d mean=%d Cb=%d..%d mean=%d Cr=%d..%d mean=%d", yMin, yMax, yMean, cbMin, cbMax, cbMean, crMin, crMax, crMean)

	reducer, err := cellreduce.New(cellreduce.Geometry{Columns: 80, Rows: 22, Fill: true})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := reducer.ReduceYCbCr(cellreduce.YCbCr{
		Y: frame.Y.Pix, Cb: frame.Cb.Pix, Cr: frame.Cr.Pix,
		Width: frame.Y.Width, Height: frame.Y.Height, YStride: frame.Y.Stride,
		ChromaWidth: frame.Cb.Width, ChromaHeight: frame.Cb.Height,
		CbStride: frame.Cb.Stride, CrStride: frame.Cr.Stride,
	})
	if err != nil {
		t.Fatal(err)
	}
	solver, err := cellframe.NewSolver(cellframe.Config{Columns: 80, Rows: 22, Mode: cellframe.Detailed})
	if err != nil {
		t.Fatal(err)
	}
	result, err := solver.SolveStatistics(stats, cellframe.SourceMeta{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Release()
	colors := make(map[cellframe.RGB]struct{})
	chromatic := 0
	for index := 0; index < result.Len(); index++ {
		cell, _ := result.Cell(index)
		colors[cell.Foreground()] = struct{}{}
		colors[cell.Background()] = struct{}{}
	}
	for value := range colors {
		lo := min(value.R(), value.G(), value.B())
		hi := max(value.R(), value.G(), value.B())
		if hi-lo >= 8 {
			chromatic++
		}
	}
	t.Logf("canonical frame has %d unique truecolors (%d visibly chromatic)", len(colors), chromatic)
	if len(colors) <= 2 {
		t.Fatalf("native camera collapsed to %d colors", len(colors))
	}
	if chromatic == 0 {
		t.Fatal("native camera produced no visibly chromatic cell colors")
	}
}
