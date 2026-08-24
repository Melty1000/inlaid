package cellreduce

import (
	"errors"
	"fmt"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

// ColorRange identifies how eight-bit luma and chroma values are encoded.
type ColorRange uint8

const (
	ColorRangeFull ColorRange = iota + 1
	ColorRangeVideo
)

// ColorMatrix identifies the YCbCr-to-RGB conversion coefficients.
type ColorMatrix uint8

const (
	ColorMatrixBT601 ColorMatrix = iota + 1
	ColorMatrixBT709
)

// NV12 is an eight-bit 4:2:0 source with a full-resolution luma plane and an
// interleaved CbCr plane. Either stride may include right-side padding.
type NV12 struct {
	Y, UV         []byte
	Width, Height int
	YStride       int
	UVStride      int
	Range         ColorRange
	Matrix        ColorMatrix
}

type ycbcrConverter struct {
	yOffset int32
	yScale  int32
	rCr     int32
	gCb     int32
	gCr     int32
	bCb     int32
}

const (
	colorShift = 16
	colorScale = 1 << colorShift
	colorHalf  = colorScale >> 1
)

// ReduceNV12 maps an NV12 source directly into cell statistics. Conversion is
// limited to source samples selected by the terminal geometry.
func (r *Reducer) ReduceNV12(source NV12) (cellframe.StatisticsFrame, error) {
	if err := validateNV12(source); err != nil {
		return cellframe.StatisticsFrame{}, err
	}
	converter := converterFor(source.Range, source.Matrix)
	clear(r.stats)
	plan := r.prepareSamplingPlan(source.Width, source.Height)
	for _, outputRow := range plan.rows {
		for _, outputColumn := range plan.columns {
			stats := &r.stats[outputRow.statOffset+outputColumn.statOffset]
			for y := outputRow.start; y < outputRow.end; y++ {
				yOffset := y * source.YStride
				uvOffset := (y / 2) * source.UVStride
				for x := outputColumn.start; x < outputColumn.end; x++ {
					chromaOffset := uvOffset + (x &^ 1)
					r, g, b := converter.rgb(source.Y[yOffset+x], source.UV[chromaOffset], source.UV[chromaOffset+1])
					stats.AddRGB(r, g, b)
				}
			}
		}
	}
	return r.frame(), nil
}

func validateNV12(source NV12) error {
	if err := validatePlaneShape(source.Width, source.Height); err != nil {
		return fmt.Errorf("cellreduce: NV12: %w", err)
	}
	if source.Range != ColorRangeFull && source.Range != ColorRangeVideo {
		return errors.New("cellreduce: NV12 color range must be full or video")
	}
	if source.Matrix != ColorMatrixBT601 && source.Matrix != ColorMatrixBT709 {
		return errors.New("cellreduce: NV12 color matrix must be BT.601 or BT.709")
	}
	uvRowBytes := ((source.Width + 1) / 2) * 2
	uvHeight := (source.Height + 1) / 2
	if source.YStride < source.Width || source.UVStride < uvRowBytes ||
		!hasBytes(source.Y, source.YStride, source.Height, source.Width) ||
		!hasBytes(source.UV, source.UVStride, uvHeight, uvRowBytes) {
		return errors.New("cellreduce: NV12 buffer is truncated")
	}
	return nil
}

func converterFor(colorRange ColorRange, matrix ColorMatrix) ycbcrConverter {
	if colorRange == ColorRangeFull {
		if matrix == ColorMatrixBT709 {
			return ycbcrConverter{yScale: 65536, rCr: 103206, gCb: -12277, gCr: -30679, bCb: 121609}
		}
		return ycbcrConverter{yScale: 65536, rCr: 91881, gCb: -22554, gCr: -46802, bCb: 116130}
	}
	if matrix == ColorMatrixBT709 {
		return ycbcrConverter{yOffset: 16, yScale: 76309, rCr: 117489, gCb: -13975, gCr: -34925, bCb: 138438}
	}
	return ycbcrConverter{yOffset: 16, yScale: 76309, rCr: 104597, gCb: -25675, gCr: -53279, bCb: 132201}
}

func (c ycbcrConverter) rgb(y, cb, cr byte) (byte, byte, byte) {
	luma := (int32(y) - c.yOffset) * c.yScale
	blueDifference := int32(cb) - 128
	redDifference := int32(cr) - 128
	return fixedByte(luma + c.rCr*redDifference),
		fixedByte(luma + c.gCb*blueDifference + c.gCr*redDifference),
		fixedByte(luma + c.bCb*blueDifference)
}

func fixedByte(value int32) byte {
	value += colorHalf
	if uint32(value)&0xff000000 == 0 {
		return byte(value >> colorShift)
	}
	return byte(^(value >> 31))
}
