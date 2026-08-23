// Package colorfx provides immutable, data-only color transforms for terminal
// cell frames. It intentionally does not execute shader or scripting code.
package colorfx

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Melty1000/inlaid/internal/cellframe"
)

const (
	// MaxCubeBytes is the largest accepted .cube source, including comments.
	MaxCubeBytes = 16 << 20
	maxLineBytes = 250
	max1DSize    = 65_536
	max3DSize    = 65
	maxCubeValue = 1e37
)

type rgb64 struct {
	r float64
	g float64
	b float64
}

type domain struct {
	min rgb64
	max rgb64
}

var unitDomain = domain{max: rgb64{r: 1, g: 1, b: 1}}

// LUT is an immutable parsed .cube transform. It can contain a 1D table, a 3D
// table, or a 1D shaper followed by a 3D table. Incoming RGB uses normalized
// sRGB code values, not linear-light intensities. Tables may use Adobe's
// finite extended range; output is clamped only after the complete transform.
type LUT struct {
	title       string
	oneD        []rgb64
	threeD      []rgb64
	oneDRange   domain
	threeDRange domain
	threeDSize  int
}

// Title returns the optional TITLE header without quotes.
func (l *LUT) Title() string {
	if l == nil {
		return ""
	}
	return l.title
}

// OneDSize returns the number of entries in the 1D shaper table.
func (l *LUT) OneDSize() int {
	if l == nil {
		return 0
	}
	return len(l.oneD)
}

// ThreeDSize returns one edge length of the 3D table.
func (l *LUT) ThreeDSize() int {
	if l == nil {
		return 0
	}
	return l.threeDSize
}

// EntryCount returns the aggregate number of retained 1D and 3D table rows.
// It lets a catalog impose one process-wide memory budget across many LUTs.
func (l *LUT) EntryCount() int {
	if l == nil {
		return 0
	}
	return len(l.oneD) + len(l.threeD)
}

// RetainedBytes returns the exact storage used by numeric table entries. LUT
// metadata is separately bounded to a few fixed fields and one short title.
func (l *LUT) RetainedBytes() int64 {
	const bytesPerEntry = int64(3 * 8) // three float64 code values
	return int64(l.EntryCount()) * bytesPerEntry
}

// ParseCube reads and validates an Adobe/Resolve-style .cube file. Parsing is
// strictly bounded; the returned LUT never retains memory owned by the reader.
func ParseCube(r io.Reader) (*LUT, error) {
	if r == nil {
		return nil, errors.New("colorfx: nil .cube reader")
	}
	return parseCubeReader(r)
}

// ParseCubeBytes validates a complete in-memory .cube file.
func ParseCubeBytes(data []byte) (*LUT, error) {
	if len(data) > MaxCubeBytes {
		return nil, fmt.Errorf("colorfx: .cube exceeds %d-byte limit", MaxCubeBytes)
	}
	return parseCubeReader(bytes.NewReader(data))
}

// LoadCube opens a regular file and parses it with the same strict bounds as
// ParseCube. File discovery and path policy remain the caller's responsibility.
func LoadCube(path string) (*LUT, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("colorfx: open .cube: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("colorfx: stat .cube: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("colorfx: .cube path is not a regular file")
	}
	return ParseCube(file)
}

type cubeParser struct {
	title string

	oneDSize   int
	threeDSize int

	titleSeen       bool
	oneDSizeSeen    bool
	threeDSizeSeen  bool
	domainMinSeen   bool
	domainMaxSeen   bool
	oneDRangeSeen   bool
	threeDRangeSeen bool

	domainMin   rgb64
	domainMax   rgb64
	oneDRange   domain
	threeDRange domain

	dataStarted bool
	expected    int
	rows        []rgb64
}

func parseCubeReader(source io.Reader) (*LUT, error) {
	// ReadSlice reuses one tiny buffer. In particular, a file containing
	// millions of empty lines cannot allocate millions of slice headers as
	// bytes.Split would. The extra five bytes allow BOM + 250 bytes + CRLF.
	limited := &io.LimitedReader{R: source, N: MaxCubeBytes + 1}
	reader := bufio.NewReaderSize(limited, maxLineBytes+5)
	p := cubeParser{oneDRange: unitDomain, threeDRange: unitDomain}
	var totalBytes int64
	for lineNumber := 1; ; lineNumber++ {
		raw, readErr := reader.ReadSlice('\n')
		totalBytes += int64(len(raw))
		if totalBytes > MaxCubeBytes {
			return nil, fmt.Errorf("colorfx: .cube exceeds %d-byte limit", MaxCubeBytes)
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			return nil, fmt.Errorf("colorfx: line %d exceeds %d-byte limit", lineNumber, maxLineBytes)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("colorfx: read .cube: %w", readErr)
		}
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			raw = raw[:len(raw)-1]
		}
		if len(raw) > 0 && raw[len(raw)-1] == '\r' {
			raw = raw[:len(raw)-1]
		}
		if lineNumber == 1 && len(raw) >= 3 && bytes.Equal(raw[:3], []byte{0xef, 0xbb, 0xbf}) {
			raw = raw[3:]
		}
		if len(raw) > maxLineBytes {
			return nil, fmt.Errorf("colorfx: line %d exceeds %d-byte limit", lineNumber, maxLineBytes)
		}
		if !utf8.Valid(raw) {
			return nil, fmt.Errorf("colorfx: line %d: .cube is not valid UTF-8", lineNumber)
		}
		if err := validateLine(raw); err != nil {
			return nil, fmt.Errorf("colorfx: line %d: %w", lineNumber, err)
		}
		line, err := stripComment(string(raw))
		if err != nil {
			return nil, fmt.Errorf("colorfx: line %d: %w", lineNumber, err)
		}
		line = strings.TrimSpace(line)
		if line != "" {
			if err := p.parseLine(line, lineNumber); err != nil {
				return nil, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return p.finish()
}

func validateLine(line []byte) error {
	for _, b := range line {
		if b == 0 {
			return errors.New("NUL byte is not allowed")
		}
		if (b < 0x20 && b != '\t') || b == 0x7f {
			return fmt.Errorf("control byte 0x%02x is not allowed", b)
		}
	}
	if bytes.Contains(line, []byte{0xef, 0xbb, 0xbf}) {
		return errors.New("UTF-8 BOM is only allowed at the start of the file")
	}
	for _, r := range string(line) {
		if r != '\t' && unicode.IsControl(r) {
			return fmt.Errorf("Unicode control character U+%04X is not allowed", r)
		}
	}
	return nil
}

func stripComment(line string) (string, error) {
	inQuote := false
	escaped := false
	for index, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if inQuote && r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			inQuote = !inQuote
			continue
		}
		if r == '#' && !inQuote {
			return line[:index], nil
		}
	}
	if inQuote || escaped {
		return "", errors.New("unterminated quoted string")
	}
	return line, nil
}

func (p *cubeParser) parseLine(line string, lineNumber int) error {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	if isHeader(fields[0]) {
		if p.dataStarted {
			return fmt.Errorf("colorfx: line %d: header %s appears after LUT data", lineNumber, fields[0])
		}
		if err := p.parseHeader(line, fields); err != nil {
			return fmt.Errorf("colorfx: line %d: %w", lineNumber, err)
		}
		return nil
	}
	if _, err := strconv.ParseFloat(fields[0], 64); err != nil {
		return fmt.Errorf("colorfx: line %d: unknown header %q", lineNumber, fields[0])
	}
	if !p.dataStarted {
		if err := p.beginData(); err != nil {
			return fmt.Errorf("colorfx: line %d: %w", lineNumber, err)
		}
	}
	if len(fields) != 3 {
		return fmt.Errorf("colorfx: line %d: LUT row must contain exactly three values", lineNumber)
	}
	if len(p.rows) >= p.expected {
		return fmt.Errorf("colorfx: line %d: LUT has more than %d data rows", lineNumber, p.expected)
	}
	row, err := parseTableRGB(fields)
	if err != nil {
		return fmt.Errorf("colorfx: line %d: %w", lineNumber, err)
	}
	p.rows = append(p.rows, row)
	return nil
}

func isHeader(token string) bool {
	switch token {
	case "TITLE", "LUT_1D_SIZE", "LUT_3D_SIZE", "DOMAIN_MIN", "DOMAIN_MAX", "LUT_1D_INPUT_RANGE", "LUT_3D_INPUT_RANGE":
		return true
	default:
		return false
	}
}

func (p *cubeParser) parseHeader(line string, fields []string) error {
	switch fields[0] {
	case "TITLE":
		if p.titleSeen {
			return errors.New("duplicate TITLE header")
		}
		p.titleSeen = true
		title, err := parseTitle(line)
		if err != nil {
			return err
		}
		p.title = title
	case "LUT_1D_SIZE":
		if p.oneDSizeSeen {
			return errors.New("duplicate LUT_1D_SIZE header")
		}
		p.oneDSizeSeen = true
		size, err := parseSize(fields, max1DSize)
		if err != nil {
			return fmt.Errorf("LUT_1D_SIZE: %w", err)
		}
		p.oneDSize = size
	case "LUT_3D_SIZE":
		if p.threeDSizeSeen {
			return errors.New("duplicate LUT_3D_SIZE header")
		}
		p.threeDSizeSeen = true
		size, err := parseSize(fields, max3DSize)
		if err != nil {
			return fmt.Errorf("LUT_3D_SIZE: %w", err)
		}
		p.threeDSize = size
	case "DOMAIN_MIN":
		if p.domainMinSeen {
			return errors.New("duplicate DOMAIN_MIN header")
		}
		p.domainMinSeen = true
		value, err := parseFiniteRGB(fields[1:])
		if len(fields) != 4 || err != nil {
			return errors.New("DOMAIN_MIN must contain exactly three finite values")
		}
		p.domainMin = value
	case "DOMAIN_MAX":
		if p.domainMaxSeen {
			return errors.New("duplicate DOMAIN_MAX header")
		}
		p.domainMaxSeen = true
		value, err := parseFiniteRGB(fields[1:])
		if len(fields) != 4 || err != nil {
			return errors.New("DOMAIN_MAX must contain exactly three finite values")
		}
		p.domainMax = value
	case "LUT_1D_INPUT_RANGE":
		if p.oneDRangeSeen {
			return errors.New("duplicate LUT_1D_INPUT_RANGE header")
		}
		p.oneDRangeSeen = true
		value, err := parseScalarRange(fields)
		if err != nil {
			return fmt.Errorf("LUT_1D_INPUT_RANGE: %w", err)
		}
		p.oneDRange = value
	case "LUT_3D_INPUT_RANGE":
		if p.threeDRangeSeen {
			return errors.New("duplicate LUT_3D_INPUT_RANGE header")
		}
		p.threeDRangeSeen = true
		value, err := parseScalarRange(fields)
		if err != nil {
			return fmt.Errorf("LUT_3D_INPUT_RANGE: %w", err)
		}
		p.threeDRange = value
	}
	if (p.domainMinSeen || p.domainMaxSeen) && (p.oneDRangeSeen || p.threeDRangeSeen) {
		return errors.New("Adobe DOMAIN headers cannot be mixed with Resolve INPUT_RANGE headers")
	}
	return nil
}

func parseTitle(line string) (string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "TITLE"))
	if len(rest) < 2 || rest[0] != '"' || rest[len(rest)-1] != '"' {
		return "", errors.New("TITLE must be one quoted string")
	}
	value, err := strconv.Unquote(rest)
	if err != nil {
		return "", errors.New("TITLE contains an invalid quoted string")
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\ufeff' {
			return "", errors.New("TITLE contains a control character")
		}
	}
	return value, nil
}

func parseSize(fields []string, maximum int) (int, error) {
	if len(fields) != 2 || fields[1] == "" {
		return 0, errors.New("must contain exactly one integer")
	}
	for _, r := range fields[1] {
		if r < '0' || r > '9' {
			return 0, errors.New("must be a decimal integer")
		}
	}
	value, err := strconv.ParseUint(fields[1], 10, 32)
	if err != nil || value < 2 || value > uint64(maximum) {
		return 0, fmt.Errorf("must be within 2..%d", maximum)
	}
	return int(value), nil
}

func parseFiniteRGB(fields []string) (rgb64, error) {
	if len(fields) != 3 {
		return rgb64{}, errors.New("expected three values")
	}
	values := [3]float64{}
	for index, field := range fields {
		value, err := strconv.ParseFloat(field, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return rgb64{}, errors.New("expected finite decimal values")
		}
		if value < -maxCubeValue || value > maxCubeValue {
			return rgb64{}, errors.New("values must be within -1e37..1e37")
		}
		values[index] = value
	}
	return rgb64{r: values[0], g: values[1], b: values[2]}, nil
}

func parseTableRGB(fields []string) (rgb64, error) {
	value, err := parseFiniteRGB(fields)
	if err != nil {
		return rgb64{}, fmt.Errorf("invalid LUT values: %w", err)
	}
	return value, nil
}

func parseScalarRange(fields []string) (domain, error) {
	if len(fields) != 3 {
		return domain{}, errors.New("must contain exactly two finite values")
	}
	minimum, minErr := strconv.ParseFloat(fields[1], 64)
	maximum, maxErr := strconv.ParseFloat(fields[2], 64)
	if minErr != nil || maxErr != nil || math.IsNaN(minimum) || math.IsNaN(maximum) || math.IsInf(minimum, 0) || math.IsInf(maximum, 0) {
		return domain{}, errors.New("must contain exactly two finite values")
	}
	if minimum < -maxCubeValue || minimum > maxCubeValue || maximum < -maxCubeValue || maximum > maxCubeValue {
		return domain{}, errors.New("values must be within -1e37..1e37")
	}
	if minimum >= maximum {
		return domain{}, errors.New("minimum must be less than maximum")
	}
	return domain{
		min: rgb64{r: minimum, g: minimum, b: minimum},
		max: rgb64{r: maximum, g: maximum, b: maximum},
	}, nil
}

func (p *cubeParser) beginData() error {
	if p.oneDSize == 0 && p.threeDSize == 0 {
		return errors.New("LUT data appears before a LUT size header")
	}
	threeDCount, ok := checkedCube(p.threeDSize)
	if !ok || p.oneDSize > math.MaxInt-threeDCount {
		return errors.New("declared LUT dimensions overflow")
	}
	p.expected = p.oneDSize + threeDCount
	p.rows = make([]rgb64, 0, p.expected)
	p.dataStarted = true
	return nil
}

func checkedCube(size int) (int, bool) {
	if size == 0 {
		return 0, true
	}
	if size < 0 || size > math.MaxInt/size {
		return 0, false
	}
	square := size * size
	if square > math.MaxInt/size {
		return 0, false
	}
	return square * size, true
}

func (p *cubeParser) finish() (*LUT, error) {
	if p.oneDSize == 0 && p.threeDSize == 0 {
		return nil, errors.New("colorfx: .cube must declare LUT_1D_SIZE, LUT_3D_SIZE, or both")
	}
	if p.domainMinSeen != p.domainMaxSeen {
		return nil, errors.New("colorfx: DOMAIN_MIN and DOMAIN_MAX must be provided together")
	}
	if p.oneDRangeSeen && p.oneDSize == 0 {
		return nil, errors.New("colorfx: LUT_1D_INPUT_RANGE requires LUT_1D_SIZE")
	}
	if p.threeDRangeSeen && p.threeDSize == 0 {
		return nil, errors.New("colorfx: LUT_3D_INPUT_RANGE requires LUT_3D_SIZE")
	}
	if p.domainMinSeen {
		if p.domainMin.r >= p.domainMax.r || p.domainMin.g >= p.domainMax.g || p.domainMin.b >= p.domainMax.b {
			return nil, errors.New("colorfx: every DOMAIN_MIN channel must be less than DOMAIN_MAX")
		}
		initial := domain{min: p.domainMin, max: p.domainMax}
		if p.oneDSize > 0 {
			p.oneDRange = initial
		} else {
			p.threeDRange = initial
		}
	}
	if !p.dataStarted {
		if err := p.beginData(); err != nil {
			return nil, fmt.Errorf("colorfx: %w", err)
		}
	}
	if len(p.rows) != p.expected {
		return nil, fmt.Errorf("colorfx: LUT has %d data rows, want exactly %d", len(p.rows), p.expected)
	}
	// The parser exclusively owns rows, so immutable LUT slices can share this
	// one exact allocation instead of transiently doubling the largest table.
	oneD := p.rows[:p.oneDSize:p.oneDSize]
	threeD := p.rows[p.oneDSize:p.expected:p.expected]
	return &LUT{
		title:       p.title,
		oneD:        oneD,
		threeD:      threeD,
		oneDRange:   p.oneDRange,
		threeDRange: p.threeDRange,
		threeDSize:  p.threeDSize,
	}, nil
}

var _ cellframe.ColorTransform = (*LUT)(nil)
