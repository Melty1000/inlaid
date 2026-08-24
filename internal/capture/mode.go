package capture

import (
	"math/bits"
	"strings"
)

type modeCandidate struct {
	Mode  Mode
	Index int
}

func chooseBestMode(candidates []modeCandidate, target Mode) (modeCandidate, bool) {
	var best modeCandidate
	found := false
	for _, candidate := range candidates {
		if !validCaptureMode(candidate.Mode) || candidate.Index < 0 {
			continue
		}
		if !found || betterMode(candidate, best, target) {
			best, found = candidate, true
		}
	}
	return best, found
}

func validCaptureMode(mode Mode) bool {
	format := strings.TrimSpace(mode.Format)
	if format == "" || len(format) > 16 || mode.Width < 1 || mode.Height < 1 || mode.Width > maxDimension || mode.Height > maxDimension || mode.FPSNumerator == 0 || mode.FPSDenominator == 0 {
		return false
	}
	rateNumerator, rateDenominator := uint64(mode.FPSNumerator), uint64(mode.FPSDenominator)
	return rateNumerator >= rateDenominator && rateNumerator <= 240*rateDenominator
}

func betterMode(left, right modeCandidate, target Mode) bool {
	leftTier, rightTier := cadenceTier(left.Mode, target), cadenceTier(right.Mode, target)
	if leftTier != rightTier {
		return leftTier < rightTier
	}
	if comparison := compareAspectError(left.Mode, right.Mode, target); comparison != 0 {
		return comparison < 0
	}
	leftArea, rightArea := areaError(left.Mode, target), areaError(right.Mode, target)
	if leftArea != rightArea {
		return leftArea < rightArea
	}
	if comparison := compareFPSDistance(left.Mode, right.Mode, target); comparison != 0 {
		return comparison < 0
	}
	return left.Index < right.Index
}

// cadenceTier first keeps rates within one frame per second of the request.
// Faster modes come next because they can still be cadence-gated to the target;
// a slower native mode cannot produce the requested cadence.
func cadenceTier(mode, target Mode) int {
	difference, denominator := fpsDifference(mode, target)
	if difference <= denominator {
		return 0
	}
	if compareRate(mode, target) > 0 {
		return 1
	}
	return 2
}

func compareAspectError(left, right, target Mode) int {
	leftError := absDifference(uint64(left.Width)*uint64(target.Height), uint64(target.Width)*uint64(left.Height))
	rightError := absDifference(uint64(right.Width)*uint64(target.Height), uint64(target.Width)*uint64(right.Height))
	// The common target-height denominator cancels, leaving error/height.
	return compareFractions(leftError, uint64(left.Height), rightError, uint64(right.Height))
}

func areaError(mode, target Mode) uint64 {
	return absDifference(uint64(mode.Width)*uint64(mode.Height), uint64(target.Width)*uint64(target.Height))
}

func compareFPSDistance(left, right, target Mode) int {
	leftDifference, leftDenominator := fpsDifference(left, target)
	rightDifference, rightDenominator := fpsDifference(right, target)
	return compareFractions(leftDifference, leftDenominator, rightDifference, rightDenominator)
}

func fpsDifference(mode, target Mode) (uint64, uint64) {
	left := uint64(mode.FPSNumerator) * uint64(target.FPSDenominator)
	right := uint64(target.FPSNumerator) * uint64(mode.FPSDenominator)
	return absDifference(left, right), uint64(mode.FPSDenominator) * uint64(target.FPSDenominator)
}

func compareRate(left, right Mode) int {
	leftValue := uint64(left.FPSNumerator) * uint64(right.FPSDenominator)
	rightValue := uint64(right.FPSNumerator) * uint64(left.FPSDenominator)
	switch {
	case leftValue < rightValue:
		return -1
	case leftValue > rightValue:
		return 1
	default:
		return 0
	}
}

func compareFractions(leftNumerator, leftDenominator, rightNumerator, rightDenominator uint64) int {
	leftHigh, leftLow := bits.Mul64(leftNumerator, rightDenominator)
	rightHigh, rightLow := bits.Mul64(rightNumerator, leftDenominator)
	switch {
	case leftHigh < rightHigh || leftHigh == rightHigh && leftLow < rightLow:
		return -1
	case leftHigh > rightHigh || leftHigh == rightHigh && leftLow > rightLow:
		return 1
	default:
		return 0
	}
}

func absDifference(left, right uint64) uint64 {
	if left >= right {
		return left - right
	}
	return right - left
}
