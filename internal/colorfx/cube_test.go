package colorfx

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

func TestParseAndTransform1D(t *testing.T) {
	lut := mustParse(t, "TITLE \"Invert\"\nLUT_1D_SIZE 2\n1 1 1\n0 0 0\n")
	if got, want := lut.Title(), "Invert"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
	if lut.OneDSize() != 2 || lut.ThreeDSize() != 0 {
		t.Fatalf("sizes = %d/%d, want 2/0", lut.OneDSize(), lut.ThreeDSize())
	}
	if got, want := lut.TransformRGB(cellframe.NewRGB(0, 128, 255)), cellframe.NewRGB(255, 127, 0); got != want {
		t.Fatalf("transform = %#06x, want %#06x", got.Packed(), want.Packed())
	}
}

func TestParseAndTransform3DRedFastest(t *testing.T) {
	lut := mustParse(t, identity3DCube("LUT_3D_SIZE 2\n"))
	if lut.OneDSize() != 0 || lut.ThreeDSize() != 2 {
		t.Fatalf("sizes = %d/%d, want 0/2", lut.OneDSize(), lut.ThreeDSize())
	}
	for _, input := range []cellframe.RGB{
		cellframe.NewRGB(255, 0, 0),
		cellframe.NewRGB(0, 255, 0),
		cellframe.NewRGB(0, 0, 255),
		cellframe.NewRGB(29, 131, 244),
	} {
		if got := lut.TransformRGB(input); got != input {
			t.Fatalf("identity transform %#06x = %#06x", input.Packed(), got.Packed())
		}
	}
}

func TestTetrahedralInterpolation(t *testing.T) {
	// Only the white corner is nonzero. For r >= g >= b the selected
	// tetrahedron weights it by b; trilinear interpolation would use r*g*b.
	lut := mustParse(t, strings.Join([]string{
		"LUT_3D_SIZE 2",
		"0 0 0", "0 0 0", "0 0 0", "0 0 0",
		"0 0 0", "0 0 0", "0 0 0", "1 1 1",
	}, "\n")+"\n")
	for _, input := range []cellframe.RGB{
		cellframe.NewRGB(191, 128, 64),
		cellframe.NewRGB(191, 64, 128),
		cellframe.NewRGB(128, 191, 64),
		cellframe.NewRGB(64, 191, 128),
		cellframe.NewRGB(128, 64, 191),
		cellframe.NewRGB(64, 128, 191),
	} {
		got := lut.TransformRGB(input)
		if want := cellframe.NewRGB(64, 64, 64); got != want {
			t.Fatalf("tetrahedral output for %#06x = %#06x, want %#06x", input.Packed(), got.Packed(), want.Packed())
		}
	}
}

func TestCombined1DAnd3D(t *testing.T) {
	source := "LUT_1D_SIZE 2\nLUT_3D_SIZE 2\n1 1 1\n0 0 0\n" + identity3DRows()
	lut := mustParse(t, source)
	input := cellframe.NewRGB(12, 100, 240)
	if got, want := lut.TransformRGB(input), cellframe.NewRGB(243, 155, 15); got != want {
		t.Fatalf("combined transform = %#06x, want %#06x", got.Packed(), want.Packed())
	}
}

func TestAdobeDomainAndResolveRanges(t *testing.T) {
	adobe := mustParse(t, identity3DCube("DOMAIN_MIN 0.25 0.25 0.25\nDOMAIN_MAX 0.75 0.75 0.75\nLUT_3D_SIZE 2\n"))
	if got := adobe.TransformRGB(cellframe.NewRGB(64, 128, 191)); got != cellframe.NewRGB(0, 129, 255) {
		t.Fatalf("Adobe domain output = %#06x", got.Packed())
	}

	resolve := mustParse(t, "LUT_1D_SIZE 2\nLUT_1D_INPUT_RANGE 0.25 0.75\n0 0 0\n1 1 1\n")
	if got := resolve.TransformRGB(cellframe.NewRGB(64, 128, 191)); got != cellframe.NewRGB(0, 129, 255) {
		t.Fatalf("Resolve range output = %#06x", got.Packed())
	}
}

func TestBOMCRLFCommentsAndInputOwnership(t *testing.T) {
	data := []byte("\xef\xbb\xbf# comment\r\nTITLE \"A # title\" # tail\r\nLUT_1D_SIZE 2\r\n0 0 0\r\n1 1 1\r\n")
	lut, err := ParseCubeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	for index := range data {
		data[index] = 0
	}
	if lut.Title() != "A # title" || lut.TransformRGB(cellframe.NewRGB(17, 23, 99)) != cellframe.NewRGB(17, 23, 99) {
		t.Fatal("parsed LUT retained or misread caller-owned bytes")
	}
}

func TestCubeValidation(t *testing.T) {
	validRows := "0 0 0\n1 1 1\n"
	tests := map[string]string{
		"no size":                   validRows,
		"size before data":          "0 0 0\n",
		"small 1d":                  "LUT_1D_SIZE 1\n",
		"large 1d":                  "LUT_1D_SIZE 65537\n",
		"small 3d":                  "LUT_3D_SIZE 1\n",
		"large 3d":                  "LUT_3D_SIZE 66\n",
		"duplicate":                 "LUT_1D_SIZE 2\nLUT_1D_SIZE 2\n" + validRows,
		"unknown":                   "CREATIVE_SHADER yes\nLUT_1D_SIZE 2\n" + validRows,
		"header after data":         "LUT_1D_SIZE 2\n0 0 0\nTITLE \"late\"\n1 1 1\n",
		"too few rows":              "LUT_1D_SIZE 2\n0 0 0\n",
		"too many rows":             "LUT_1D_SIZE 2\n" + validRows + "0 0 0\n",
		"short row":                 "LUT_1D_SIZE 2\n0 0\n1 1 1\n",
		"nonfinite":                 "LUT_1D_SIZE 2\nNaN 0 0\n1 1 1\n",
		"table over positive limit": "LUT_1D_SIZE 2\n1.000001e37 0 0\n1 1 1\n",
		"table over negative limit": "LUT_1D_SIZE 2\n-1.000001e37 0 0\n1 1 1\n",
		"half domain":               "DOMAIN_MIN 0 0 0\nLUT_1D_SIZE 2\n" + validRows,
		"backwards domain":          "DOMAIN_MIN 1 0 0\nDOMAIN_MAX 0 1 1\nLUT_1D_SIZE 2\n" + validRows,
		"mixed dialects":            "DOMAIN_MIN 0 0 0\nDOMAIN_MAX 1 1 1\nLUT_1D_INPUT_RANGE 0 1\nLUT_1D_SIZE 2\n" + validRows,
		"orphan 1d range":           "LUT_1D_INPUT_RANGE 0 1\nLUT_3D_SIZE 2\n" + identity3DRows(),
		"orphan 3d range":           "LUT_3D_INPUT_RANGE 0 1\nLUT_1D_SIZE 2\n" + validRows,
		"backwards input range":     "LUT_1D_SIZE 2\nLUT_1D_INPUT_RANGE 1 1\n" + validRows,
		"input range over limit":    "LUT_1D_SIZE 2\nLUT_1D_INPUT_RANGE -1e38 1\n" + validRows,
		"domain over limit":         "DOMAIN_MIN -1e38 0 0\nDOMAIN_MAX 1 1 1\nLUT_1D_SIZE 2\n" + validRows,
		"duplicate title":           "TITLE \"a\"\nTITLE \"b\"\nLUT_1D_SIZE 2\n" + validRows,
		"unquoted title":            "TITLE hello\nLUT_1D_SIZE 2\n" + validRows,
		"embedded BOM":              "LUT_1D_SIZE 2\n\ufeff0 0 0\n1 1 1\n",
		"NUL":                       "LUT_1D_SIZE 2\n0 0\x00 0\n1 1 1\n",
		"control":                   "LUT_1D_SIZE 2\n0 0\x01 0\n1 1 1\n",
		"Unicode control":           "TITLE \"bad\u0085title\"\nLUT_1D_SIZE 2\n" + validRows,
		"escaped title control":     "TITLE \"bad\\u0000title\"\nLUT_1D_SIZE 2\n" + validRows,
		"unterminated title":        "TITLE \"oops\nLUT_1D_SIZE 2\n" + validRows,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCube(strings.NewReader(source)); err == nil {
				t.Fatal("invalid .cube succeeded")
			}
		})
	}
}

func TestCubeBoundsAndUTF8(t *testing.T) {
	longLine := "#" + strings.Repeat("x", maxLineBytes) + "\nLUT_1D_SIZE 2\n0 0 0\n1 1 1\n"
	if _, err := ParseCube(strings.NewReader(longLine)); err == nil {
		t.Fatal("overlong line succeeded")
	}
	line := append(bytes.Repeat([]byte{' '}, 249), '\n')
	oversized := bytes.Repeat(line, MaxCubeBytes/len(line)+1)
	if _, err := ParseCube(bytes.NewReader(oversized)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized streaming input error = %v", err)
	}
	if _, err := ParseCubeBytes([]byte{'L', 'U', 'T', '_', '1', 'D', '_', 'S', 'I', 'Z', 'E', ' ', '2', '\n', 0xff}); err == nil {
		t.Fatal("invalid UTF-8 succeeded")
	}
}

func TestManyBlankLinesDoNotAmplifyAllocations(t *testing.T) {
	source := bytes.Repeat([]byte{'\n'}, 50_000)
	var parseErr error
	allocations := testing.AllocsPerRun(3, func() {
		_, parseErr = ParseCubeBytes(source)
	})
	if parseErr == nil {
		t.Fatal("headerless input unexpectedly succeeded")
	}
	if allocations > 32 {
		t.Fatalf("50,000 blank lines caused %.0f allocations; want a constant bounded count", allocations)
	}
}

func TestRetainedBytesCountsOneSharedTableAllocation(t *testing.T) {
	lut := mustParse(t, "LUT_1D_SIZE 2\nLUT_3D_SIZE 2\n0 0 0\n1 1 1\n"+identity3DRows())
	if got, want := lut.EntryCount(), 10; got != want {
		t.Fatalf("EntryCount = %d, want %d", got, want)
	}
	if got, want := lut.RetainedBytes(), int64(10*3*8); got != want {
		t.Fatalf("RetainedBytes = %d, want %d", got, want)
	}
}

func TestBuiltinsAndStrength(t *testing.T) {
	input := cellframe.NewRGB(40, 100, 220)
	for _, name := range []string{"None", "Warm", "Cool", "Mono"} {
		transform, err := Builtin(name)
		if err != nil {
			t.Fatalf("Builtin(%q): %v", name, err)
		}
		output := transform.TransformRGB(input)
		if name == "None" && output != input {
			t.Fatal("None changed input")
		}
		if name == "Mono" && (output.R() != output.G() || output.G() != output.B()) {
			t.Fatalf("Mono output is not neutral: %#06x", output.Packed())
		}
	}
	if _, err := Builtin("shader"); err == nil {
		t.Fatal("unknown built-in succeeded")
	}
	mono, _ := Builtin("Mono")
	half, err := WithStrength(mono, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	full := mono.TransformRGB(input)
	want := cellframe.NewRGB(
		uint8((int(input.R())+int(full.R())+1)/2),
		uint8((int(input.G())+int(full.G())+1)/2),
		uint8((int(input.B())+int(full.B())+1)/2),
	)
	if got := half.TransformRGB(input); got != want {
		t.Fatalf("half strength = %#06x, want %#06x", got.Packed(), want.Packed())
	}
	for _, invalid := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		if _, err := WithStrength(mono, invalid); err == nil {
			t.Fatalf("invalid strength %v succeeded", invalid)
		}
	}
}

func TestExtendedTableValuesClampOnlyAfterStrength(t *testing.T) {
	limits := mustParse(t, "LUT_1D_SIZE 2\n-1e37 -1e37 -1e37\n1e37 1e37 1e37\n")
	if limits.OneDSize() != 2 {
		t.Fatal("inclusive Adobe extended bounds were rejected")
	}
	lut := mustParse(t, "LUT_1D_SIZE 2\n2 2 2\n2 2 2\n")
	if got := lut.TransformRGB(cellframe.NewRGB(0, 0, 0)); got != cellframe.NewRGB(255, 255, 255) {
		t.Fatalf("extended output = %#06x, want clamped white", got.Packed())
	}
	quarter, err := WithStrength(lut, 0.25)
	if err != nil {
		t.Fatal(err)
	}
	// 25% of extended 2.0 is 0.5. Clamping the LUT to 1.0 before
	// blending would incorrectly produce 0.25 (64) instead.
	if got := quarter.TransformRGB(cellframe.NewRGB(0, 0, 0)); got != cellframe.NewRGB(128, 128, 128) {
		t.Fatalf("extended strength output = %#06x, want 0x808080", got.Packed())
	}
	threeQuarter, err := WithStrength(lut, 0.75)
	if err != nil {
		t.Fatal(err)
	}
	nested, err := WithStrength(threeQuarter, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if got := nested.TransformRGB(cellframe.NewRGB(0, 0, 0)); got != cellframe.NewRGB(191, 191, 191) {
		t.Fatalf("nested extended strength output = %#06x, want 0xbfbfbf", got.Packed())
	}
	negative := mustParse(t, "LUT_1D_SIZE 2\n-2 -2 -2\n-2 -2 -2\n")
	if got := negative.TransformRGB(cellframe.NewRGB(255, 255, 255)); got != cellframe.NewRGB(0, 0, 0) {
		t.Fatalf("negative extended output = %#06x, want black", got.Packed())
	}
}

func TestMaximumDeclaredSizesAreAcceptedBeforeRowValidation(t *testing.T) {
	for _, header := range []string{"LUT_1D_SIZE 65536\n", "LUT_3D_SIZE 65\n"} {
		_, err := ParseCube(strings.NewReader(header))
		if err == nil || !strings.Contains(err.Error(), "data rows") {
			t.Fatalf("%q error = %v, want missing rows", strings.TrimSpace(header), err)
		}
	}
}

func BenchmarkLUT3DTetrahedral(b *testing.B) {
	lut := mustParse(b, identity3DCube("LUT_3D_SIZE 2\n"))
	input := cellframe.NewRGB(71, 137, 219)
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		input = lut.TransformRGB(input)
	}
	benchmarkColor = input
}

var benchmarkColor cellframe.RGB

func mustParse(tb testing.TB, source string) *LUT {
	tb.Helper()
	lut, err := ParseCube(strings.NewReader(source))
	if err != nil {
		tb.Fatal(err)
	}
	return lut
}

func identity3DCube(headers string) string { return headers + identity3DRows() }

func identity3DRows() string {
	var builder strings.Builder
	for b := 0; b < 2; b++ {
		for g := 0; g < 2; g++ {
			for r := 0; r < 2; r++ {
				fmt.Fprintf(&builder, "%d %d %d\n", r, g, b)
			}
		}
	}
	return builder.String()
}
