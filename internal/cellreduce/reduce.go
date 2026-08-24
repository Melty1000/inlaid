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
// either axis.
type YCbCr struct {
	Y, Cb, Cr                 []byte
	Width, Height             int
	YStride                   int
	ChromaWidth, ChromaHeight int
	CbStride, CrStride        int
}

// Reducer owns one fixed reusable statistics buffer and caches sampling plans
// for the current source layouts. It is single-stream and performs no
// allocation while a source layout remains unchanged.
type Reducer struct {
	geometry Geometry
	stats    []cellframe.SampleStats
	sampling samplingPlan
	ycbcr    ycbcrSamplingPlan
}

type sampleSpan struct {
	start, end int
	statOffset int
}

type samplingPlan struct {
	width, height int
	columns       []sampleSpan
	rows          []sampleSpan
}

type ycbcrLayout struct {
	width, height             int
	yStride                   int
	chromaWidth, chromaHeight int
	cbStride, crStride        int
}

type sourceRowOffsets struct {
	y, cb, cr int
}

type ycbcrSamplingPlan struct {
	layout     ycbcrLayout
	chromaX    []int
	sourceRows []sourceRowOffsets
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

// ReduceRGB24 maps an arbitrary reduced RGB source into cell-major quadrant
// statistics. Downscaling area-aggregates every covered source pixel;
// upscaling uses nearest source support for otherwise empty bins.
func (r *Reducer) ReduceRGB24(source RGB24) (cellframe.StatisticsFrame, error) {
	if err := validateRGB24(source); err != nil {
		return cellframe.StatisticsFrame{}, err
	}
	clear(r.stats)
	plan := r.prepareSamplingPlan(source.Width, source.Height)
	for _, outputRow := range plan.rows {
		for _, outputColumn := range plan.columns {
			stats := &r.stats[outputRow.statOffset+outputColumn.statOffset]
			for y := outputRow.start; y < outputRow.end; y++ {
				row := y * source.Stride
				for x := outputColumn.start; x < outputColumn.end; x++ {
					offset := row + x*3
					stats.AddRGB(source.Pix[offset], source.Pix[offset+1], source.Pix[offset+2])
				}
			}
		}
	}
	return r.frame(), nil
}

// ReduceYCbCr maps native reduced JPEG planes directly into cell statistics.
// Conversion occurs only for samples that contribute to the terminal grid.
func (r *Reducer) ReduceYCbCr(source YCbCr) (cellframe.StatisticsFrame, error) {
	if err := validateYCbCr(source); err != nil {
		return cellframe.StatisticsFrame{}, err
	}
	clear(r.stats)
	plan := r.prepareSamplingPlan(source.Width, source.Height)
	ycbcr := r.prepareYCbCrPlan(source)
	for _, outputRow := range plan.rows {
		for _, outputColumn := range plan.columns {
			stats := &r.stats[outputRow.statOffset+outputColumn.statOffset]
			for y := outputRow.start; y < outputRow.end; y++ {
				offsets := ycbcr.sourceRows[y]
				for x := outputColumn.start; x < outputColumn.end; x++ {
					cx := ycbcr.chromaX[x]
					r, g, b := color.YCbCrToRGB(source.Y[offsets.y+x], source.Cb[offsets.cb+cx], source.Cr[offsets.cr+cx])
					stats.AddRGB(r, g, b)
				}
			}
		}
	}
	return r.frame(), nil
}

func (r *Reducer) frame() cellframe.StatisticsFrame {
	return cellframe.StatisticsFrame{
		Quadrants: r.stats,
		Columns:   r.geometry.Columns,
		Rows:      r.geometry.Rows,
	}
}

func (r *Reducer) prepareSamplingPlan(sourceWidth, sourceHeight int) *samplingPlan {
	if r.sampling.width == sourceWidth && r.sampling.height == sourceHeight {
		return &r.sampling
	}

	cropX, cropY, cropWidth, cropHeight := sourceCrop(sourceWidth, sourceHeight, r.geometry)
	sampleColumns, sampleRows := r.geometry.Columns*2, r.geometry.Rows*2
	columns := r.sampling.columns
	if cap(columns) < sampleColumns {
		columns = make([]sampleSpan, sampleColumns)
	} else {
		columns = columns[:sampleColumns]
	}
	rows := r.sampling.rows
	if cap(rows) < sampleRows {
		rows = make([]sampleSpan, sampleRows)
	} else {
		rows = rows[:sampleRows]
	}

	for outputX := range sampleColumns {
		start, end := sourceRange(outputX, sampleColumns, cropX, cropWidth)
		displayX := outputX
		if r.geometry.Mirror {
			displayX = sampleColumns - 1 - outputX
		}
		columns[outputX] = sampleSpan{
			start:      start,
			end:        end,
			statOffset: (displayX/2)*4 + displayX&1,
		}
	}
	for outputY := 0; outputY < sampleRows; outputY++ {
		start, end := sourceRange(outputY, sampleRows, cropY, cropHeight)
		rows[outputY] = sampleSpan{
			start:      start,
			end:        end,
			statOffset: (outputY/2*r.geometry.Columns)*4 + (outputY&1)*2,
		}
	}

	r.sampling = samplingPlan{
		width:   sourceWidth,
		height:  sourceHeight,
		columns: columns,
		rows:    rows,
	}
	return &r.sampling
}

func (r *Reducer) prepareYCbCrPlan(source YCbCr) *ycbcrSamplingPlan {
	layout := ycbcrLayout{
		width:        source.Width,
		height:       source.Height,
		yStride:      source.YStride,
		chromaWidth:  source.ChromaWidth,
		chromaHeight: source.ChromaHeight,
		cbStride:     source.CbStride,
		crStride:     source.CrStride,
	}
	if r.ycbcr.layout == layout {
		return &r.ycbcr
	}

	chromaX := r.ycbcr.chromaX
	if cap(chromaX) < source.Width {
		chromaX = make([]int, source.Width)
	} else {
		chromaX = chromaX[:source.Width]
	}
	for x := range source.Width {
		chromaX[x] = x * source.ChromaWidth / source.Width
	}

	sourceRows := r.ycbcr.sourceRows
	if cap(sourceRows) < source.Height {
		sourceRows = make([]sourceRowOffsets, source.Height)
	} else {
		sourceRows = sourceRows[:source.Height]
	}
	for y := range source.Height {
		cy := y * source.ChromaHeight / source.Height
		sourceRows[y] = sourceRowOffsets{
			y:  y * source.YStride,
			cb: cy * source.CbStride,
			cr: cy * source.CrStride,
		}
	}

	r.ycbcr = ycbcrSamplingPlan{layout: layout, chromaX: chromaX, sourceRows: sourceRows}
	return &r.ycbcr
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
	rows := height - 1
	if rows > 0 && stride > (math.MaxInt-rowBytes)/rows {
		return false
	}
	return rows*stride+rowBytes <= len(data)
}

// FitGeometry chooses a whole-camera grid inside maxColumns/maxRows, then
// applies the same bounded cell budget used by New. Fill uses the entire
// requested interior until that budget is reached.
func FitGeometry(sourceWidth, sourceHeight, maxColumns, maxRows int, fill bool) Geometry {
	maxColumns = min(max(maxColumns, 1), MaxCells)
	maxRows = min(max(maxRows, 1), MaxCells)
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
