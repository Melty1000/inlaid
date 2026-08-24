package dashboard

import (
	"fmt"
	"image"
	"image/draw"
	"math"
	"strings"

	"github.com/Melty1000/inlaid/internal/render"
	"github.com/charmbracelet/x/mosaic"
)

// PreviewSource produces the deterministic demo shown when no live runtime is
// attached. Real camera frames arrive through Runtime previews.
type PreviewSource interface {
	Frame(columns, rows int, sequence uint64, symbolName, quality string, fill, mirror bool) string
}

type demoPreview struct {
	lastKey   string
	lastFrame string
}

func newDemoPreview() PreviewSource {
	return &demoPreview{}
}

func (d *demoPreview) Frame(columns, rows int, sequence uint64, symbolName, quality string, fill, mirror bool) string {
	columns = max(columns, 1)
	rows = max(rows, 1)
	key := fmt.Sprintf("%dx%d/%d/%s/%s/%t/%t", columns, rows, sequence, symbolName, quality, fill, mirror)
	if key == d.lastKey {
		return d.lastFrame
	}

	threshold, dither := previewQuality(quality)
	renderer, err := render.New(render.Config{
		Columns:   columns,
		Rows:      rows,
		Symbols:   previewSymbol(symbolName),
		Threshold: threshold,
		Dither:    dither,
	})
	if err != nil {
		return strings.Repeat(" ", columns)
	}
	var img image.Image = syntheticPortrait(320, 180, sequence)
	if fill {
		img = cropToAspect(img, float64(columns)/(2*float64(rows)))
	}
	if mirror {
		img = mirrorImage(img)
	}
	d.lastKey = key
	d.lastFrame = renderer.Render(img)
	return d.lastFrame
}

func previewQuality(name string) (threshold int, dither bool) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "SMOOTH":
		return 104, false
	case "SHARP":
		return 150, true
	default:
		return 128, false
	}
}

func cropToAspect(img image.Image, targetAspect float64) image.Image {
	bounds := img.Bounds()
	if targetAspect <= 0 || bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return img
	}
	sourceAspect := float64(bounds.Dx()) / float64(bounds.Dy())
	crop := bounds
	if targetAspect > sourceAspect {
		height := max(int(math.Round(float64(bounds.Dx())/targetAspect)), 1)
		top := bounds.Min.Y + (bounds.Dy()-height)/2
		crop = image.Rect(bounds.Min.X, top, bounds.Max.X, top+height)
	} else if targetAspect < sourceAspect {
		width := max(int(math.Round(float64(bounds.Dy())*targetAspect)), 1)
		left := bounds.Min.X + (bounds.Dx()-width)/2
		crop = image.Rect(left, bounds.Min.Y, left+width, bounds.Max.Y)
	}
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba.SubImage(crop)
	}
	return img
}

func mirrorImage(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for x := 0; x < bounds.Dx(); x++ {
		sourceX := bounds.Max.X - 1 - x
		draw.Draw(dst, image.Rect(x, 0, x+1, bounds.Dy()), src, image.Pt(sourceX, bounds.Min.Y), draw.Src)
	}
	return dst
}

func previewSymbol(name string) mosaic.Symbol {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "half":
		return mosaic.Half
	case "all":
		return mosaic.All
	default:
		return mosaic.Quarter
	}
}

// syntheticPortrait is intentionally recognizable as a camera-like test feed,
// but never confusable with a real webcam frame.
func syntheticPortrait(width, height int, sequence uint64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	phase := float64(sequence%240) / 240 * math.Pi * 2
	scanY := int(sequence*3) % height
	for y := 0; y < height; y++ {
		fy := float64(y) / float64(height-1)
		for x := 0; x < width; x++ {
			fx := float64(x) / float64(width-1)
			r, g, b := 10, 13, 17

			// Matte studio wall, equipment columns, and a violet practical.
			if x < width/5 && y > height/6 {
				r, g, b = 16, 23, 29
				if x%36 < 3 || y%31 < 2 {
					r, g, b = 31, 44, 52
				}
			}
			if x > width*4/5 && y > height/5 {
				r, g, b = 23, 16, 32
				if x%29 < 2 {
					r, g, b = 58, 35, 74
				}
			}

			// Shoulders and shirt.
			if ellipse(fx, fy, 0.52, 1.03, 0.36, 0.40) {
				r, g, b = 54, 66, 78
				if int((fx+fy)*80)%19 < 2 {
					r, g, b = 65, 80, 93
				}
			}

			// Neck, face, and hair.
			if ellipse(fx, fy, 0.52, 0.62, 0.095, 0.20) {
				r, g, b = 151, 140, 137
			}
			if ellipse(fx, fy, 0.52, 0.43, 0.16, 0.29) {
				light := int(14 * math.Sin(fx*8+phase))
				r, g, b = 166+light, 155+light, 151+light
			}
			if ellipse(fx, fy, 0.50, 0.29, 0.17, 0.13) || (ellipse(fx, fy, 0.42, 0.39, 0.055, 0.19) && fx < 0.42) {
				r, g, b = 27, 25, 31
			}

			// Glasses, eyes, nose, and beard create a legible focal target.
			if rectBand(fx, fy, 0.405, 0.485, 0.17, 0.09, 0.008) || rectBand(fx, fy, 0.545, 0.485, 0.17, 0.09, 0.008) || (fy > 0.47 && fy < 0.49 && fx > 0.49 && fx < 0.55) {
				r, g, b = 43, 50, 60
			}
			if ellipse(fx, fy, 0.465, 0.49, 0.014, 0.013) || ellipse(fx, fy, 0.58, 0.49, 0.014, 0.013) {
				r, g, b = 100, 222, 232
			}
			if fx > 0.515 && fx < 0.535 && fy > 0.49 && fy < 0.61 {
				r, g, b = 131, 119, 120
			}
			if ellipse(fx, fy, 0.52, 0.65, 0.13, 0.11) && fy > 0.59 {
				r, g, b = 38, 31, 36
			}
			if ellipse(fx, fy, 0.52, 0.62, 0.047, 0.025) {
				r, g, b = 104, 69, 77
			}

			// A subtle scan line makes the deterministic feed visibly alive.
			if abs(y-scanY) <= 1 {
				r = min(r+10, 255)
				g = min(g+28, 255)
				b = min(b+30, 255)
			}

			offset := y*img.Stride + x*4
			img.Pix[offset+0] = uint8(clampInt(r, 0, 255))
			img.Pix[offset+1] = uint8(clampInt(g, 0, 255))
			img.Pix[offset+2] = uint8(clampInt(b, 0, 255))
			img.Pix[offset+3] = 255
		}
	}
	return img
}

func ellipse(x, y, cx, cy, rx, ry float64) bool {
	dx, dy := (x-cx)/rx, (y-cy)/ry
	return dx*dx+dy*dy <= 1
}

func rectBand(x, y, cx, cy, w, h, edge float64) bool {
	dx, dy := math.Abs(x-cx), math.Abs(y-cy)
	inside := dx <= w/2 && dy <= h/2
	inner := dx < w/2-edge && dy < h/2-edge
	return inside && !inner
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func clampInt(v, low, high int) int {
	return min(max(v, low), high)
}
