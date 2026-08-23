// Package cellreduce maps reduced camera planes directly into the sufficient
// statistics consumed by cellframe. It deliberately never constructs an
// intermediate image or a 2C-by-2R bitmap.
package cellreduce

import (
	"errors"
	"fmt"
	"image/color"
	"math"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

const (
	// MaxCells bounds the reusable statistics allocation. It is deliberately
	// above a 300x84 full-screen grid while preventing extreme terminal zoom
	// or forged resize events from allocating without limit.
	MaxCells          = 40_000
	maxPlaneDimension = 8192
	maxPlanePixels    = 7680 * 4320
)

// Geometry describes how a source maps into a fixed terminal grid.
type Geometry struct {
	Columns int
	Rows    int
	Fill    bool
	Mirror  bool
}

// RGB24 is an interleaved reduced RGB source.
type RGB24 struct {
	Pix           []byte
	Width, Height int
	Stride        int
}

// YCbCr is a reduced planar source. Chroma may be independently subsampled in
// either axis; WIC's native JPEG path currently returns 4:2:2 planes.
type YCbCr struct {
	Y, Cb, Cr                 []byte
	Width, Height             int
	YStride                   int
	ChromaWidth, ChromaHeight int
	CbStride, CrStride        int
}

// Reducer owns one fixed reusable statistics buffer. It is single-stream and
// performs no allocation after construction.
type Reducer struct {
	geometry Geometry
	stats    []cellframe.SampleStats
}

// New validates a bounded terminal geometry and allocates its one reusable
// sufficient-statistics buffer.
func New(geometry Geometry) (*Reducer, error) {
	if geometry.Columns <= 0 || geometry.Rows <= 0 {
		return nil, errors.New("cellreduce: columns and rows must be positive")
	}
	if geometry.Columns > MaxCells/geometry.Rows {
		return nil, fmt.Errorf("cellreduce: grid must not exceed %d cells", MaxCells)
	}
	return &Reducer{
		geometry: geometry,
		stats:    make([]cellframe.SampleStats, geometry.Columns*geometry.Rows*4),
	}, nil
}

// Geometry returns the reducer's fixed mapping.
func (r *Reducer) Geometry() Geometry { return r.geometry }

// ReduceRGB24 maps an arbitrary reduced RGB source into cell-major quadrant
// statistics. Downscaling area-aggregates every covered source pixel;
// upscaling uses nearest source support for otherwise empty bins.
func (r *Reducer) ReduceRGB24(source RGB24) (cellframe.StatisticsFrame, error) {
	if err := validateRGB24(source); err != nil {
		return cellframe.StatisticsFrame{}, err
	}
	clear(r.stats)
	r.reduce(source.Width, source.Height, func(x, y int) (uint8, uint8, uint8) {
		offset := y*source.Stride + x*3
		return source.Pix[offset], source.Pix[offset+1], source.Pix[offset+2]
	})
	return r.frame(), nil
}

// ReduceYCbCr maps native reduced JPEG planes directly into cell statistics.
// Conversion occurs only for samples that contribute to the terminal grid.
func (r *Reducer) ReduceYCbCr(source YCbCr) (cellframe.StatisticsFrame, error) {
	if err := validateYCbCr(source); err != nil {
		return cellframe.StatisticsFrame{}, err
	}
	clear(r.stats)
	r.reduce(source.Width, source.Height, func(x, y int) (uint8, uint8, uint8) {
		cx := min(x*source.ChromaWidth/source.Width, source.ChromaWidth-1)
		cy := min(y*source.ChromaHeight/source.Height, source.ChromaHeight-1)
		return color.YCbCrToRGB(
			source.Y[y*source.YStride+x],
			source.Cb[cy*source.CbStride+cx],
			source.Cr[cy*source.CrStride+cx],
		)
	})
	return r.frame(), nil
}

func (r *Reducer) frame() cellframe.StatisticsFrame {
	return cellframe.StatisticsFrame{
		Quadrants: r.stats,
		Columns:   r.geometry.Columns,
		Rows:      r.geometry.Rows,
	}
}

type sampler func(x, y int) (uint8, uint8, uint8)

func (r *Reducer) reduce(sourceWidth, sourceHeight int, sample sampler) {
	cropX, cropY, cropWidth, cropHeight := sourceCrop(sourceWidth, sourceHeight, r.geometry)
	sampleColumns, sampleRows := r.geometry.Columns*2, r.geometry.Rows*2
	for outputY := 0; outputY < sampleRows; outputY++ {
		y0, y1 := sourceRange(outputY, sampleRows, cropY, cropHeight)
		cellY, quadrantY := outputY/2, outputY&1
		for outputX := 0; outputX < sampleColumns; outputX++ {
			x0, x1 := sourceRange(outputX, sampleColumns, cropX, cropWidth)
			cellX, quadrantX := outputX/2, outputX&1
			quadrant := quadrantY*2 + quadrantX
			stats := &r.stats[(cellY*r.geometry.Columns+cellX)*4+quadrant]
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					r, g, b := sample(x, y)
					stats.AddRGB(r, g, b)
				}
			}
		}
	}
	if r.geometry.Mirror {
		r.mirrorStatistics()
	}
}

func (r *Reducer) mirrorStatistics() {
	columns, rows := r.geometry.Columns, r.geometry.Rows
	for y := 0; y < rows; y++ {
		for left := 0; left < (columns+1)/2; left++ {
			right := columns - 1 - left
			leftBase := (y*columns + left) * 4
			rightBase := (y*columns + right) * 4
			if left == right {
				r.stats[leftBase], r.stats[leftBase+1] = r.stats[leftBase+1], r.stats[leftBase]
				r.stats[leftBase+2], r.stats[leftBase+3] = r.stats[leftBase+3], r.stats[leftBase+2]
				continue
			}
			for row := 0; row < 2; row++ {
				l0, l1 := leftBase+row*2, leftBase+row*2+1
				r0, r1 := rightBase+row*2, rightBase+row*2+1
				r.stats[l0], r.stats[r1] = r.stats[r1], r.stats[l0]
				r.stats[l1], r.stats[r0] = r.stats[r0], r.stats[l1]
			}
		}
	}
}

func sourceCrop(width, height int, geometry Geometry) (x, y, cropWidth, cropHeight int) {
	if !geometry.Fill {
		return 0, 0, width, height
	}
	targetAspect := float64(geometry.Columns) / (2 * float64(geometry.Rows))
	sourceAspect := float64(width) / float64(height)
	if targetAspect > sourceAspect {
		cropHeight = max(int(math.Round(float64(width)/targetAspect)), 1)
		return 0, (height - cropHeight) / 2, width, cropHeight
	}
	if targetAspect < sourceAspect {
		cropWidth = max(int(math.Round(float64(height)*targetAspect)), 1)
		return (width - cropWidth) / 2, 0, cropWidth, height
	}
	return 0, 0, width, height
}

func sourceRange(output, outputSize, sourceStart, sourceSize int) (int, int) {
	start := sourceStart + output*sourceSize/outputSize
	end := sourceStart + (output+1)*sourceSize/outputSize
	if end > start {
		return start, end
	}
	// The output is finer than the reduced source. Choose the nearest pixel at
	// the bin center instead of leaving an invalid empty statistic.
	center := sourceStart + ((2*output+1)*sourceSize)/(2*outputSize)
	center = min(max(center, sourceStart), sourceStart+sourceSize-1)
	return center, center + 1
}

func validateRGB24(source RGB24) error {
	if err := validatePlaneShape(source.Width, source.Height); err != nil {
		return fmt.Errorf("cellreduce: RGB24: %w", err)
	}
	rowBytes := source.Width * 3
	if source.Stride < rowBytes || !hasBytes(source.Pix, source.Stride, source.Height, rowBytes) {
		return errors.New("cellreduce: RGB24 buffer is truncated")
	}
	return nil
}

func validateYCbCr(source YCbCr) error {
	if err := validatePlaneShape(source.Width, source.Height); err != nil {
		return fmt.Errorf("cellreduce: luma: %w", err)
	}
	if err := validatePlaneShape(source.ChromaWidth, source.ChromaHeight); err != nil {
		return fmt.Errorf("cellreduce: chroma: %w", err)
	}
	if source.ChromaWidth > source.Width || source.ChromaHeight > source.Height {
		return errors.New("cellreduce: chroma dimensions exceed luma")
	}
	if source.YStride < source.Width || source.CbStride < source.ChromaWidth || source.CrStride < source.ChromaWidth ||
		!hasBytes(source.Y, source.YStride, source.Height, source.Width) ||
		!hasBytes(source.Cb, source.CbStride, source.ChromaHeight, source.ChromaWidth) ||
		!hasBytes(source.Cr, source.CrStride, source.ChromaHeight, source.ChromaWidth) {
		return errors.New("cellreduce: planar YCbCr buffer is truncated")
	}
	return nil
}

func validatePlaneShape(width, height int) error {
	if width <= 0 || height <= 0 || width > maxPlaneDimension || height > maxPlaneDimension {
		return errors.New("invalid plane dimensions")
	}
	if width > maxPlanePixels/height {
		return errors.New("plane exceeds pixel bound")
	}
	return nil
}

func hasBytes(data []byte, stride, height, rowBytes int) bool {
	if stride < 0 || height <= 0 || rowBytes < 0 {
		return false
	}
	required := int64(height-1)*int64(stride) + int64(rowBytes)
	return required >= 0 && required <= int64(len(data))
}

// FitGeometry chooses a whole-camera grid inside maxColumns/maxRows, then
// applies the same bounded cell budget used by New. Fill uses the entire
// requested interior until that budget is reached.
func FitGeometry(sourceWidth, sourceHeight, maxColumns, maxRows int, fill bool) Geometry {
	maxColumns, maxRows = max(maxColumns, 1), max(maxRows, 1)
	columns, rows := maxColumns, maxRows
	if !fill && sourceWidth > 0 && sourceHeight > 0 {
		aspect := float64(sourceWidth) / float64(sourceHeight)
		rows = max(int(math.Floor(float64(columns)/(2*aspect))), 1)
		if rows > maxRows {
			rows = maxRows
			columns = max(int(math.Floor(float64(rows)*2*aspect)), 1)
		}
	}
	if columns > MaxCells/rows {
		scale := math.Sqrt(float64(MaxCells) / (float64(columns) * float64(rows)))
		columns = max(int(float64(columns)*scale), 1)
		rows = max(int(float64(rows)*scale), 1)
		for columns > MaxCells/rows {
			if columns >= rows {
				columns--
			} else {
				rows--
			}
		}
	}
	return Geometry{Columns: columns, Rows: rows, Fill: fill}
}
