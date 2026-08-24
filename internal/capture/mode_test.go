package capture

import "testing"

func TestChooseBestModeExact(t *testing.T) {
	target := testMode(1920, 1080, 30, 1)
	want := modeCandidate{Mode: target, Index: 4}
	got, ok := chooseBestMode([]modeCandidate{
		{Mode: testMode(1920, 1080, 30000, 1001), Index: 1},
		want,
		{Mode: testMode(1280, 720, 30, 1), Index: 0},
	}, target)
	if !ok || got != want {
		t.Fatalf("chooseBestMode = %+v, %t; want %+v, true", got, ok, want)
	}
}

func TestChooseBestModeTreatsNTSCThirtyAsNearTarget(t *testing.T) {
	target := testMode(1920, 1080, 30, 1)
	want := modeCandidate{Mode: testMode(1920, 1080, 30000, 1001), Index: 8}
	got, ok := chooseBestMode([]modeCandidate{
		{Mode: testMode(1920, 1080, 60, 1), Index: 1},
		want,
		{Mode: testMode(1280, 720, 30, 1), Index: 2},
	}, target)
	if !ok || got != want {
		t.Fatalf("chooseBestMode = %+v, %t; want %+v, true", got, ok, want)
	}
	if got.Mode.NominalFPS() != 30 || got.Mode.FPSNumerator != 30000 || got.Mode.FPSDenominator != 1001 {
		t.Fatalf("rational rate was not preserved: %+v", got.Mode)
	}
}

func TestChooseBestModeFallsBackToMatchingAspect720p(t *testing.T) {
	target := testMode(1920, 1080, 30, 1)
	want := modeCandidate{Mode: testMode(1280, 720, 30, 1), Index: 3}
	got, ok := chooseBestMode([]modeCandidate{
		{Mode: testMode(1600, 1200, 30, 1), Index: 0},
		{Mode: testMode(640, 360, 30, 1), Index: 1},
		want,
	}, target)
	if !ok || got != want {
		t.Fatalf("chooseBestMode = %+v, %t; want %+v, true", got, ok, want)
	}
}

func TestChooseBestModeCadenceTiers(t *testing.T) {
	target := testMode(1920, 1080, 30, 1)
	near := modeCandidate{Mode: testMode(1280, 720, 30000, 1001), Index: 2}
	above := modeCandidate{Mode: testMode(1920, 1080, 60, 1), Index: 1}
	below := modeCandidate{Mode: testMode(1920, 1080, 28, 1), Index: 0}
	if got, _ := chooseBestMode([]modeCandidate{below, above, near}, target); got != near {
		t.Fatalf("near-target tier lost: %+v", got)
	}
	if got, _ := chooseBestMode([]modeCandidate{below, above}, target); got != above {
		t.Fatalf("above-target tier should precede below-target tier: %+v", got)
	}
}

func TestChooseBestModeRanksAspectThenArea(t *testing.T) {
	target := testMode(1920, 1080, 30, 1)
	matchingAspect := modeCandidate{Mode: testMode(960, 540, 30, 1), Index: 5}
	closerAreaWrongAspect := modeCandidate{Mode: testMode(1600, 1200, 30, 1), Index: 0}
	if got, _ := chooseBestMode([]modeCandidate{closerAreaWrongAspect, matchingAspect}, target); got != matchingAspect {
		t.Fatalf("aspect ranking lost: %+v", got)
	}
	closerArea := modeCandidate{Mode: testMode(1280, 720, 30, 1), Index: 9}
	if got, _ := chooseBestMode([]modeCandidate{matchingAspect, closerArea}, target); got != closerArea {
		t.Fatalf("area ranking lost: %+v", got)
	}
}

func TestChooseBestModeUsesStableNativeIndex(t *testing.T) {
	target := testMode(1920, 1080, 30, 1)
	first := modeCandidate{Mode: testMode(1280, 720, 30, 1), Index: 2}
	second := modeCandidate{Mode: first.Mode, Index: 7}
	if got, _ := chooseBestMode([]modeCandidate{second, first}, target); got != first {
		t.Fatalf("stable-index tiebreak lost: %+v", got)
	}
}

func TestChooseBestModeRejectsInvalidModes(t *testing.T) {
	target := testMode(1920, 1080, 30, 1)
	if got, ok := chooseBestMode([]modeCandidate{
		{Mode: testMode(1920, 1080, 30, 0), Index: 0},
		{Mode: testMode(1920, 1080, 1, 2), Index: 1},
		{Mode: Mode{Width: 1920, Height: 1080, FPSNumerator: 30, FPSDenominator: 1}, Index: 2},
	}, target); ok {
		t.Fatalf("invalid mode selected: %+v", got)
	}
}

func TestChooseBestModeAcceptsPlatformNativeFormats(t *testing.T) {
	target := Mode{Width: 1920, Height: 1080, FPSNumerator: 30, FPSDenominator: 1, Format: "NV12"}
	want := modeCandidate{Mode: target, Index: 2}
	if got, ok := chooseBestMode([]modeCandidate{want}, target); !ok || got != want {
		t.Fatalf("chooseBestMode = %+v, %t; want %+v, true", got, ok, want)
	}
}

func TestModeNominalFPSPreservesRationalRate(t *testing.T) {
	mode := testMode(1920, 1080, 30000, 1001)
	if got := mode.NominalFPS(); got != 30 {
		t.Fatalf("NominalFPS = %d, want 30", got)
	}
	if got := mode.FPS(); got < 29.96 || got > 29.98 {
		t.Fatalf("FPS = %.6f, want about 29.97", got)
	}
}

func testMode(width, height int, numerator, denominator uint32) Mode {
	return Mode{Width: width, Height: height, FPSNumerator: numerator, FPSDenominator: denominator, Format: "MJPG"}
}
