package dashboard

import (
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/Melty1000/inlaid/internal/cellframe"
	"github.com/Melty1000/inlaid/internal/cellrender"
	"github.com/charmbracelet/x/ansi"
)

func TestTrustedLiveFrameSpliceMatchesDefensiveProjection(t *testing.T) {
	runtime := NewRuntime(DefaultSettings(), filepath.Join(t.TempDir(), "settings.json"), t.TempDir())
	t.Cleanup(func() { _ = runtime.Close() })
	m := NewLive(DefaultSettings(), runtime, nil)
	m.SetSize(126, 47)
	m.liveColumns, m.liveRows = 96, 27
	m.liveANSI = highEntropyANSI(t, m.liveColumns, m.liveRows, 17)
	m.refreshRender()

	interior, _, _ := m.previewInterior(m.previewCacheW, m.previewCacheH)
	reference := m
	reference.stitchInteriorRows(interior)
	if m.rendered != reference.rendered {
		t.Fatal("trusted live-frame splice changed the defensive projection")
	}
	if got := lipgloss.Height(m.rendered); got != m.height {
		t.Fatalf("rendered height = %d, want %d", got, m.height)
	}
	if got := lipgloss.Width(m.rendered); got > m.width-1 {
		t.Fatalf("rendered width = %d, want at most %d", got, m.width-1)
	}

	hitMap := m.hitMap
	m.liveANSI = highEntropyANSI(t, m.liveColumns, m.liveRows, 91)
	m.refreshPreviewRender()
	if m.hitMap != hitMap {
		t.Fatal("live-frame splice rebuilt the dashboard hit map")
	}
	if got := lipgloss.Height(m.rendered); got != m.height {
		t.Fatalf("updated height = %d, want %d", got, m.height)
	}
	if got := lipgloss.Width(m.rendered); got > m.width-1 {
		t.Fatalf("updated width = %d, want at most %d", got, m.width-1)
	}
}

func TestMalformedLiveFrameUsesDefensiveProjection(t *testing.T) {
	runtime := NewRuntime(DefaultSettings(), filepath.Join(t.TempDir(), "settings.json"), t.TempDir())
	t.Cleanup(func() { _ = runtime.Close() })
	m := NewLive(DefaultSettings(), runtime, nil)
	m.SetSize(120, 38)
	m.liveColumns, m.liveRows = 8, 3
	m.liveANSI = "too short\nsecond row"
	if m.stitchTrustedLiveRows() {
		t.Fatal("malformed live frame bypassed the defensive projection")
	}
	m.stitchPreviewRows()

	if got := lipgloss.Height(m.rendered); got != m.height {
		t.Fatalf("fallback height = %d, want %d", got, m.height)
	}
	if got := lipgloss.Width(m.rendered); got > m.width-1 {
		t.Fatalf("fallback width = %d, want at most %d", got, m.width-1)
	}

	m.liveANSI = ""
	m.liveColumns, m.liveRows = 0, 0
	m.cameraState = "connecting"
	m.stitchPreviewRows()
	if !strings.Contains(ansi.Strip(m.rendered), "CONNECTING TO CAMERA") {
		t.Fatal("empty live frame did not preserve the status-content fallback")
	}
}

func BenchmarkDashboardLiveFrameSpliceHighEntropy240x67(b *testing.B) {
	runtime := NewRuntime(DefaultSettings(), filepath.Join(b.TempDir(), "settings.json"), b.TempDir())
	b.Cleanup(func() { _ = runtime.Close() })
	m := NewLive(DefaultSettings(), runtime, nil)
	m.SetSize(246, 81)
	m.liveColumns, m.liveRows = 240, 67
	frames := [2]string{
		highEntropyANSI(b, m.liveColumns, m.liveRows, 0),
		highEntropyANSI(b, m.liveColumns, m.liveRows, 73),
	}
	for index, frame := range frames {
		styles := strings.Count(frame, "\x1b[38;2;")
		if styles < m.liveColumns*m.liveRows*9/10 {
			b.Fatalf("frame %d has only %d styles for %d cells", index, styles, m.liveColumns*m.liveRows)
		}
	}
	m.liveANSI = frames[0]
	m.refreshRender()
	b.ReportAllocs()
	b.SetBytes(int64(len(frames[0])))
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		m.liveANSI = frames[i&1]
		m.refreshPreviewRender()
	}
}

func BenchmarkMosaicDemo160x31(b *testing.B) {
	benchmarkMosaicDemo(b, 160, 31)
}

func BenchmarkMosaicDemo240x67(b *testing.B) {
	benchmarkMosaicDemo(b, 240, 67)
}

func benchmarkMosaicDemo(b *testing.B, columns, rows int) {
	preview := newDemoPreview()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		frame := preview.Frame(columns, rows, uint64(i), "quarter", "AUTO", true, false)
		if frame == "" {
			b.Fatal("demo preview returned an empty frame")
		}
	}
}

func highEntropyANSI(tb testing.TB, columns, rows, phase int) string {
	tb.Helper()
	solver, err := cellframe.NewSolver(cellframe.Config{
		Columns: columns,
		Rows:    rows,
		Mode:    cellframe.ModeDetailed,
		Buffers: 1,
	})
	if err != nil {
		tb.Fatal(err)
	}
	width, height := columns*2, rows*2
	pixels := make([]byte, width*height*3)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			offset := (y*width + x) * 3
			pixels[offset] = byte(x*37 + y*17 + phase)
			pixels[offset+1] = byte(x*11 + y*53 + phase*3)
			pixels[offset+2] = byte(x*71 + y*29 + phase*5)
		}
	}
	frame, err := solver.SolveRGB24(cellframe.RGB24{
		Pix: pixels, Width: width, Height: height, Stride: width * 3,
	}, cellframe.SourceMeta{})
	if err != nil {
		tb.Fatal(err)
	}
	defer frame.Release()
	ansi, err := cellrender.ANSI(frame)
	if err != nil {
		tb.Fatal(err)
	}
	return ansi
}
