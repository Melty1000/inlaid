package colorfx

import (
	"errors"
	"math"
	"strings"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

// TransformRGB applies the optional 1D shaper and then the optional 3D table.
// A nil LUT is the identity transform.
func (l *LUT) TransformRGB(color cellframe.RGB) cellframe.RGB {
	if l == nil {
		return color
	}
	value := l.transformNormalized(normalizedRGB(color))
	return cellframe.NewRGB(toByte(value.r), toByte(value.g), toByte(value.b))
}

func (l *LUT) transformNormalized(value rgb64) rgb64 {
	if l == nil {
		return value
	}
	if len(l.oneD) != 0 {
		value = sample1D(l.oneD, l.oneDRange, value)
	}
	if l.threeDSize != 0 {
		value = sampleTetrahedral(l.threeD, l.threeDSize, l.threeDRange, value)
	}
	return value
}

func normalizedRGB(color cellframe.RGB) rgb64 {
	return rgb64{
		r: float64(color.R()) / 255,
		g: float64(color.G()) / 255,
		b: float64(color.B()) / 255,
	}
}

func sample1D(table []rgb64, input domain, value rgb64) rgb64 {
	return rgb64{
		r: sample1DChannel(table, normalize(value.r, input.min.r, input.max.r), 0),
		g: sample1DChannel(table, normalize(value.g, input.min.g, input.max.g), 1),
		b: sample1DChannel(table, normalize(value.b, input.min.b, input.max.b), 2),
	}
}

func sample1DChannel(table []rgb64, value float64, channel int) float64 {
	position := value * float64(len(table)-1)
	lower := int(position)
	if lower >= len(table)-1 {
		return component(table[len(table)-1], channel)
	}
	fraction := position - float64(lower)
	a := component(table[lower], channel)
	b := component(table[lower+1], channel)
	return a + fraction*(b-a)
}

func component(value rgb64, channel int) float64 {
	switch channel {
	case 0:
		return value.r
	case 1:
		return value.g
	default:
		return value.b
	}
}

func sampleTetrahedral(table []rgb64, size int, input domain, value rgb64) rgb64 {
	r0, dr := latticeCoordinate(normalize(value.r, input.min.r, input.max.r), size)
	g0, dg := latticeCoordinate(normalize(value.g, input.min.g, input.max.g), size)
	b0, db := latticeCoordinate(normalize(value.b, input.min.b, input.max.b), size)
	at := func(r, g, b int) rgb64 { return table[r+size*(g+size*b)] }
	c000 := at(r0, g0, b0)

	// The six branches partition the unit cube into tetrahedra. Keeping red as
	// the fastest table axis matches .cube row ordering used by Adobe/Resolve.
	if dr >= dg {
		if dg >= db {
			return tetra(c000, at(r0+1, g0, b0), at(r0+1, g0+1, b0), at(r0+1, g0+1, b0+1), dr, dg, db)
		}
		if dr >= db {
			return tetra(c000, at(r0+1, g0, b0), at(r0+1, g0, b0+1), at(r0+1, g0+1, b0+1), dr, db, dg)
		}
		return tetra(c000, at(r0, g0, b0+1), at(r0+1, g0, b0+1), at(r0+1, g0+1, b0+1), db, dr, dg)
	}
	if db >= dg {
		return tetra(c000, at(r0, g0, b0+1), at(r0, g0+1, b0+1), at(r0+1, g0+1, b0+1), db, dg, dr)
	}
	if db >= dr {
		return tetra(c000, at(r0, g0+1, b0), at(r0, g0+1, b0+1), at(r0+1, g0+1, b0+1), dg, db, dr)
	}
	return tetra(c000, at(r0, g0+1, b0), at(r0+1, g0+1, b0), at(r0+1, g0+1, b0+1), dg, dr, db)
}

func tetra(c0, c1, c2, c3 rgb64, f1, f2, f3 float64) rgb64 {
	return rgb64{
		r: c0.r + f1*(c1.r-c0.r) + f2*(c2.r-c1.r) + f3*(c3.r-c2.r),
		g: c0.g + f1*(c1.g-c0.g) + f2*(c2.g-c1.g) + f3*(c3.g-c2.g),
		b: c0.b + f1*(c1.b-c0.b) + f2*(c2.b-c1.b) + f3*(c3.b-c2.b),
	}
}

func latticeCoordinate(value float64, size int) (int, float64) {
	position := value * float64(size-1)
	lower := int(position)
	if lower >= size-1 {
		return size - 2, 1
	}
	return lower, position - float64(lower)
}

func normalize(value, minimum, maximum float64) float64 {
	value = (value - minimum) / (maximum - minimum)
	if value <= 0 {
		return 0
	}
	if value >= 1 {
		return 1
	}
	return value
}

func toByte(value float64) uint8 {
	if value <= 0 {
		return 0
	}
	if value >= 1 {
		return 255
	}
	return uint8(math.Floor(value*255 + 0.5))
}

type identityTransform struct{}

func (identityTransform) TransformRGB(color cellframe.RGB) cellframe.RGB { return color }

type builtinTransform uint8

const (
	builtinWarm builtinTransform = iota + 1
	builtinCool
	builtinMono
)

// Builtin returns one of the small, deterministic looks None, Warm, Cool, or
// Mono. Names are matched case-insensitively.
func Builtin(name string) (cellframe.ColorTransform, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "none":
		return identityTransform{}, nil
	case "warm":
		return builtinTransform(builtinWarm), nil
	case "cool":
		return builtinTransform(builtinCool), nil
	case "mono":
		return builtinTransform(builtinMono), nil
	default:
		return nil, errors.New("colorfx: unknown built-in look")
	}
}

func (kind builtinTransform) TransformRGB(color cellframe.RGB) cellframe.RGB {
	r, g, b := int(color.R()), int(color.G()), int(color.B())
	switch kind {
	case builtinWarm:
		return cellframe.NewRGB(clampByte((266*r+5*g)/256+3), clampByte((3*r+253*g)/256+1), clampByte((241*b+2*g)/256-2))
	case builtinCool:
		return cellframe.NewRGB(clampByte((241*r+2*g)/256-2), clampByte((3*b+253*g)/256+1), clampByte((266*b+5*g)/256+3))
	case builtinMono:
		luma := clampByte((54*r + 183*g + 19*b + 128) >> 8)
		return cellframe.NewRGB(luma, luma, luma)
	default:
		return color
	}
}

func clampByte(value int) uint8 {
	if value <= 0 {
		return 0
	}
	if value >= 255 {
		return 255
	}
	return uint8(value)
}

type strengthTransform struct {
	inner  cellframe.ColorTransform
	amount uint32
}

type normalizedTransform interface {
	transformNormalized(rgb64) rgb64
}

type normalizedStrengthTransform struct {
	inner    normalizedTransform
	strength float64
}

// WithStrength blends a transform with its input. Strength must be finite and
// within 0..1. The returned immutable wrapper uses fixed-point blending and
// performs no allocation in TransformRGB.
func WithStrength(transform cellframe.ColorTransform, strength float64) (cellframe.ColorTransform, error) {
	if transform == nil {
		return nil, errors.New("colorfx: nil transform")
	}
	if math.IsNaN(strength) || math.IsInf(strength, 0) || strength < 0 || strength > 1 {
		return nil, errors.New("colorfx: strength must be within 0..1")
	}
	if strength == 0 {
		return identityTransform{}, nil
	}
	if strength == 1 {
		return transform, nil
	}
	if normalized, ok := transform.(normalizedTransform); ok {
		return normalizedStrengthTransform{inner: normalized, strength: strength}, nil
	}
	return strengthTransform{inner: transform, amount: uint32(math.Floor(strength*65_535 + 0.5))}, nil
}

func (transform normalizedStrengthTransform) TransformRGB(input cellframe.RGB) cellframe.RGB {
	source := normalizedRGB(input)
	output := transform.transformNormalized(source)
	return cellframe.NewRGB(toByte(output.r), toByte(output.g), toByte(output.b))
}

func (transform normalizedStrengthTransform) transformNormalized(source rgb64) rgb64 {
	output := transform.inner.transformNormalized(source)
	output.r = source.r + (output.r-source.r)*transform.strength
	output.g = source.g + (output.g-source.g)*transform.strength
	output.b = source.b + (output.b-source.b)*transform.strength
	return output
}

func (transform strengthTransform) TransformRGB(input cellframe.RGB) cellframe.RGB {
	output := transform.inner.TransformRGB(input)
	return cellframe.NewRGB(
		blendChannel(input.R(), output.R(), transform.amount),
		blendChannel(input.G(), output.G(), transform.amount),
		blendChannel(input.B(), output.B(), transform.amount),
	)
}

func blendChannel(input, output uint8, amount uint32) uint8 {
	const maximum = uint32(65_535)
	return uint8((uint32(input)*(maximum-amount) + uint32(output)*amount + maximum/2) / maximum)
}

var (
	_ cellframe.ColorTransform = identityTransform{}
	_ cellframe.ColorTransform = builtinTransform(0)
	_ cellframe.ColorTransform = strengthTransform{}
	_ cellframe.ColorTransform = normalizedStrengthTransform{}
)
