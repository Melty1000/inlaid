// Package recording streams fixed-size RGBA frames to one persistent FFmpeg
// process. Encoders only write private, same-directory staging files. A
// completed, non-empty result is atomically published when the writer closes.
// GIF uses a lossless FFV1/Matroska source for its palette-quantization pass.
package recording

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ffmpegDiagnosticLimit = 64 << 10
	ffmpegEncoderThreads  = 2
	ffmpegFilterThreads   = 1
	maxMediaDimension     = 8192
	maxMediaPixels        = 7680 * 4320
	maxTerminalCells      = 40_000
	minFinalizeTimeout    = 2 * time.Minute
)

// Format identifies the requested final container.
type Format string

const (
	FormatMP4 Format = "mp4"
	FormatGIF Format = "gif"
)

// Config controls a fixed-rate recording.
type Config struct {
	FFmpeg string
	Output string
	Width  int
	Height int
	FPS    int
	Format Format

	// CellColumns/CellRows switch the input contract from a full canvas to a
	// canonical 2C-by-2R quadrant raster. FFmpeg enlarges those exact samples
	// with nearest-neighbor and centers them on Width-by-Height; no extra image
	// detail is invented and Go never allocates a full video frame.
	CellColumns int
	CellRows    int

	// Overwrite allows replacing an existing Output. The default is safe and
	// refuses to replace a file.
	Overwrite bool

	// CRF controls H.264 quality. Zero selects 16.
	CRF int
	// GIFColors controls the generated palette. Zero selects 256.
	GIFColors int

	// DiscardCompletedStageOnFailure declares that the caller already owns a
	// durable recovery source. When publishing or GIF conversion fails, the
	// private completed MP4/FFV1 stage is then redundant and is removed instead
	// of being retained beside that source. The zero value preserves the
	// generic Writer contract: callers without another recovery source keep the
	// completed stage and receive its path in the error.
	DiscardCompletedStageOnFailure bool
}

// Writer owns one FFmpeg rawvideo process. Each WriteFrame contributes exactly
// one frame at Config.FPS; callers should write at their chosen real-time
// cadence.
type Writer struct {
	cfg        Config
	ctx        context.Context
	stdin      io.WriteCloser
	command    *exec.Cmd
	stderr     *byteTail
	stagePath  string
	wait       chan error
	closeDone  chan struct{}
	closeOnce  sync.Once
	writeMu    sync.Mutex
	closing    atomic.Bool
	frames     atomic.Uint64
	closeError error
}

// Start launches FFmpeg and prepares its raw RGBA stdin pipe.
func Start(ctx context.Context, cfg Config) (*Writer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, err := normalize(cfg)
	if err != nil {
		return nil, err
	}

	outputDir := filepath.Dir(normalized.Output)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create recording directory: %w", err)
	}
	if !normalized.Overwrite {
		if _, statErr := os.Stat(normalized.Output); statErr == nil {
			return nil, fmt.Errorf("recording output already exists: %s", normalized.Output)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect recording output: %w", statErr)
		}
	}

	stagePattern := ".inlaid-recording-*.mp4"
	if normalized.Format == FormatGIF {
		stagePattern = ".inlaid-recording-*.mkv"
	}
	stage, createErr := os.CreateTemp(outputDir, stagePattern)
	if createErr != nil {
		return nil, fmt.Errorf("create recording staging file: %w", createErr)
	}
	stagePath := stage.Name()
	if closeErr := stage.Close(); closeErr != nil {
		_ = os.Remove(stagePath)
		return nil, fmt.Errorf("close recording staging file: %w", closeErr)
	}

	// The path belongs exclusively to this Writer, so FFmpeg may replace the
	// zero-byte file created securely by CreateTemp. Overwrite policy applies
	// only when the completed stage is published to cfg.Output.
	encodeConfig := normalized
	encodeConfig.Overwrite = true
	args := rawEncodeArgs(encodeConfig, stagePath)
	command := exec.Command(normalized.FFmpeg, args...)
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = os.Remove(stagePath)
		return nil, fmt.Errorf("open FFmpeg input: %w", err)
	}
	writer := &Writer{
		cfg:       normalized,
		ctx:       ctx,
		stdin:     stdin,
		command:   command,
		stderr:    newByteTail(ffmpegDiagnosticLimit),
		stagePath: stagePath,
		wait:      make(chan error, 1),
		closeDone: make(chan struct{}),
	}
	command.Stderr = writer.stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = os.Remove(stagePath)
		return nil, fmt.Errorf("start FFmpeg: %w", err)
	}
	// The explicit FFmpeg thread ceilings are the primary resource guard. On
	// supported hosts, also make the encoder yield CPU time to the interactive
	// terminal.
	// Priority changes are best-effort because locked-down systems can deny the
	// process-information handle even though encoding itself is healthy.
	_ = lowerEncoderPriority(command.Process)
	go func() {
		writer.wait <- command.Wait()
		close(writer.wait)
	}()
	go func() {
		select {
		case <-ctx.Done():
			// Cancellation means the caller did not accept a complete recording.
			// Kill and discard the private stage; a graceful Close here could
			// publish a perfectly valid but misleadingly shortened MP4.
			_ = writer.Abort()
		case <-writer.closeDone:
		}
	}()
	return writer, nil
}

// WriteFrame writes one fixed-size RGBA frame. Non-zero bounds and padded
// strides are handled row by row without changing the image.
func (w *Writer) WriteFrame(frame *image.RGBA) error {
	if w == nil {
		return errors.New("recording writer is nil")
	}
	if frame == nil {
		return errors.New("recording frame is nil")
	}
	expectedWidth, expectedHeight := inputDimensions(w.cfg)
	if frame.Bounds().Dx() != expectedWidth || frame.Bounds().Dy() != expectedHeight {
		return fmt.Errorf(
			"recording frame is %dx%d; expected %dx%d",
			frame.Bounds().Dx(),
			frame.Bounds().Dy(),
			expectedWidth,
			expectedHeight,
		)
	}
	if w.closing.Load() {
		return errors.New("recording writer is closed")
	}

	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if w.closing.Load() {
		return errors.New("recording writer is closed")
	}
	bounds := frame.Bounds()
	rowBytes := bounds.Dx() * 4
	if frame.Stride == rowBytes && bounds.Min.X == 0 && bounds.Min.Y == 0 {
		if err := writeAll(w.stdin, frame.Pix[:rowBytes*bounds.Dy()]); err != nil {
			return err
		}
		w.frames.Add(1)
		return nil
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		offset := frame.PixOffset(bounds.Min.X, y)
		if err := writeAll(w.stdin, frame.Pix[offset:offset+rowBytes]); err != nil {
			return err
		}
	}
	w.frames.Add(1)
	return nil
}

// Close finishes the video stream and waits for a playable output. For GIF it
// also performs the palette-generation pass. Close is idempotent and safe to
// call concurrently with context cancellation.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.closing.Store(true)
		// Check before waiting for writeMu: a blocked pipe writer is released by
		// terminating FFmpeg, and canceled work must never be finalized.
		if w.ctx != nil && w.ctx.Err() != nil && w.command != nil && w.command.Process != nil {
			_ = w.command.Process.Kill()
		}
		w.writeMu.Lock()
		stdinErr := w.stdin.Close()
		w.writeMu.Unlock()

		finalizeTimeout := encoderFinalizeTimeout(w.frames.Load(), w.cfg.FPS)
		finalizeTimer := time.NewTimer(finalizeTimeout)
		defer finalizeTimer.Stop()
		var processErr error
		select {
		case processErr = <-w.wait:
		case <-finalizeTimer.C:
			timeoutErr := fmt.Errorf(
				"FFmpeg did not finish within %s after its input closed",
				finalizeTimeout.Round(time.Second),
			)
			var killErr error
			if w.command != nil && w.command.Process != nil {
				killErr = w.command.Process.Kill()
				if errors.Is(killErr, os.ErrProcessDone) {
					killErr = nil
				}
			}
			var killedProcessErr error
			select {
			case killedProcessErr = <-w.wait:
			case <-time.After(5 * time.Second):
				killedProcessErr = errors.New("FFmpeg process did not report exit after it was killed")
			}
			processErr = errors.Join(timeoutErr, killErr, killedProcessErr)
		}
		if w.ctx != nil && w.ctx.Err() != nil {
			w.closeError = errors.Join(
				fmt.Errorf("recording canceled: %w", w.ctx.Err()),
				processErr,
				w.discardStage(),
			)
		} else if processErr != nil {
			w.closeError = errors.Join(
				ffmpegError("encode recording", processErr, w.stderr.String()),
				w.discardStage(),
			)
		} else if stdinErr != nil {
			w.closeError = errors.Join(
				fmt.Errorf("close FFmpeg input: %w", stdinErr),
				w.discardStage(),
			)
		} else if err := validateMediaStage(w.stagePath); err != nil {
			w.closeError = errors.Join(err, w.discardStage())
		} else if w.cfg.Format == FormatGIF {
			w.closeError = w.finishGIF()
		} else {
			w.closeError = w.finishMP4()
		}
		close(w.closeDone)
	})
	<-w.closeDone
	return w.closeError
}

// encoderFinalizeTimeout keeps a hung encoder bounded without penalizing a
// legitimate long recording. Local MP4 output does not use faststart, so close
// normally writes only trailing container metadata; two minutes is already a
// generous floor. Above roughly 2.5 hours the budget grows by one second per
// 100 seconds of recorded media.
func encoderFinalizeTimeout(frames uint64, fps int) time.Duration {
	if fps <= 0 {
		return minFinalizeTimeout
	}
	recordedSeconds := frames / uint64(fps)
	const base = 30 * time.Second
	maxExtraSeconds := uint64((time.Duration(1<<63-1) - base) / time.Second)
	if recordedSeconds/100 > maxExtraSeconds {
		return time.Duration(1<<63 - 1)
	}
	timeout := base + time.Duration(recordedSeconds/100)*time.Second
	return max(timeout, minFinalizeTimeout)
}

// Abort stops FFmpeg without promising a finalized output. It is intended for
// unrecoverable caller errors; normal cancellation should use Close.
func (w *Writer) Abort() error {
	if w == nil || w.command == nil || w.command.Process == nil {
		return nil
	}
	// Kill even when Close is already waiting for writeMu. A blocked pipe write
	// owns that mutex; terminating FFmpeg is what unblocks it and lets Close
	// finish. Calling Close without the kill would defeat the caller's timeout.
	var killErr error
	if err := w.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		killErr = fmt.Errorf("stop FFmpeg: %w", err)
	}
	return errors.Join(killErr, w.Close())
}

// Output returns the final requested output path.
func (w *Writer) Output() string {
	if w == nil {
		return ""
	}
	return w.cfg.Output
}

func (w *Writer) finishGIF() error {
	outputDir := filepath.Dir(w.cfg.Output)
	temp, err := os.CreateTemp(outputDir, ".inlaid-gif-*.gif")
	if err != nil {
		return w.recoverableGIFError("create temporary GIF output", err)
	}
	tempOutput := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempOutput)
		return w.recoverableGIFError("close temporary GIF output", err)
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tempOutput)
		}
	}()

	args := gifConvertArgs(w.cfg, w.stagePath, tempOutput)
	// GIF palette conversion scales with recording duration. A fixed two-minute
	// deadline destroyed otherwise healthy long recordings, so allow at least
	// four times the captured duration (with a two-minute floor).
	recorded := time.Duration(w.frames.Load()) * time.Second / time.Duration(w.cfg.FPS)
	conversionTimeout := max(2*time.Minute, recorded*4+30*time.Second)
	baseContext := w.ctx
	if baseContext == nil {
		baseContext = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseContext, conversionTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, w.cfg.FFmpeg, args...)
	stderr := newByteTail(ffmpegDiagnosticLimit)
	command.Stderr = stderr
	err = command.Start()
	if err == nil {
		_ = lowerEncoderPriority(command.Process)
		err = command.Wait()
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("GIF conversion exceeded %s: %w", conversionTimeout.Round(time.Second), err)
		} else if errors.Is(ctx.Err(), context.Canceled) {
			err = fmt.Errorf("GIF conversion canceled: %w", err)
		}
		return w.recoverableGIFError("convert lossless stage to GIF", ffmpegError("create GIF", err, stderr.String()))
	}
	if err := validateMediaStage(tempOutput); err != nil {
		return w.recoverableGIFError("validate temporary GIF output", err)
	}
	if err := publishMedia(tempOutput, w.cfg.Output, w.cfg.Overwrite); err != nil {
		return w.recoverableGIFError("publish GIF output", err)
	}
	keepTemp = true
	// The final GIF is already complete and published. A transient cleanup
	// failure must not report that recording as failed and trigger a duplicate
	// recovery export; the private stage is best-effort cleanup at this point.
	_ = w.discardStage()
	return nil
}

func (w *Writer) finishMP4() error {
	if err := publishMedia(w.stagePath, w.cfg.Output, w.cfg.Overwrite); err != nil {
		if w.cfg.DiscardCompletedStageOnFailure {
			return errors.Join(
				fmt.Errorf("publish MP4 output: %w", err),
				w.discardStage(),
			)
		}
		return fmt.Errorf("publish MP4 output: %w (recoverable completed stage kept at %s)", err, w.stagePath)
	}
	w.stagePath = ""
	return nil
}

func (w *Writer) recoverableGIFError(action string, err error) error {
	if w.cfg.DiscardCompletedStageOnFailure {
		return errors.Join(
			fmt.Errorf("%s: %w", action, err),
			w.discardStage(),
		)
	}
	return fmt.Errorf("%s: %w (recoverable lossless FFV1 stage kept at %s)", action, err, w.stagePath)
}

func (w *Writer) discardStage() error {
	if w.stagePath == "" {
		return nil
	}
	err := os.Remove(w.stagePath)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		w.stagePath = ""
		return nil
	}
	return fmt.Errorf("remove incomplete recording stage %s: %w", w.stagePath, err)
}

func validateMediaStage(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect recording stage: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("recording stage is not a regular file: %s", path)
	}
	if info.Size() == 0 {
		return errors.New("encoder produced an empty recording stage")
	}
	return nil
}

func normalize(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.FFmpeg) == "" {
		return Config{}, errors.New("FFmpeg executable is required")
	}
	if strings.TrimSpace(cfg.Output) == "" {
		return Config{}, errors.New("recording output is required")
	}
	absoluteOutput, err := filepath.Abs(cfg.Output)
	if err != nil {
		return Config{}, fmt.Errorf("resolve recording output: %w", err)
	}
	cfg.Output = absoluteOutput
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Config{}, errors.New("recording width and height must be positive")
	}
	if cfg.Width > maxMediaDimension || cfg.Height > maxMediaDimension {
		return Config{}, fmt.Errorf("recording width and height must not exceed %d pixels", maxMediaDimension)
	}
	if cfg.Width > maxMediaPixels/cfg.Height {
		return Config{}, fmt.Errorf("recording canvas must not exceed %d pixels", maxMediaPixels)
	}
	if (cfg.CellColumns == 0) != (cfg.CellRows == 0) {
		return Config{}, errors.New("recording cell columns and rows must be set together")
	}
	if cfg.CellColumns < 0 || cfg.CellRows < 0 {
		return Config{}, errors.New("recording cell columns and rows must not be negative")
	}
	if cfg.CellColumns > 0 {
		if cfg.CellColumns > maxMediaDimension/2 || cfg.CellRows > maxMediaDimension/2 {
			return Config{}, errors.New("recording cell grid is too large")
		}
		if cfg.CellColumns > maxTerminalCells/cfg.CellRows {
			return Config{}, fmt.Errorf("recording grid must not exceed %d cells", maxTerminalCells)
		}
		if cfg.Width&1 != 0 || cfg.Height&1 != 0 {
			return Config{}, errors.New("cell recording canvas width and height must be even")
		}
		if _, _, _, _, err := cellCanvasGeometry(cfg.CellColumns, cfg.CellRows, cfg.Width, cfg.Height); err != nil {
			return Config{}, err
		}
	}
	if cfg.FPS <= 0 || cfg.FPS > 240 {
		return Config{}, errors.New("recording FPS must be between 1 and 240")
	}
	if cfg.Format == "" {
		switch strings.ToLower(filepath.Ext(cfg.Output)) {
		case ".gif":
			cfg.Format = FormatGIF
		case ".mp4":
			cfg.Format = FormatMP4
		default:
			return Config{}, errors.New("recording format must be mp4 or gif")
		}
	}
	if cfg.Format != FormatMP4 && cfg.Format != FormatGIF {
		return Config{}, fmt.Errorf("unsupported recording format %q", cfg.Format)
	}
	expectedExtension := "." + string(cfg.Format)
	if extension := strings.ToLower(filepath.Ext(cfg.Output)); extension != expectedExtension {
		return Config{}, fmt.Errorf("%s recording output must use the %s extension", cfg.Format, expectedExtension)
	}
	if cfg.CRF == 0 {
		cfg.CRF = 16
	}
	if cfg.CRF < 0 || cfg.CRF > 51 {
		return Config{}, errors.New("recording CRF must be between 0 and 51")
	}
	if cfg.GIFColors == 0 {
		cfg.GIFColors = 256
	}
	if cfg.GIFColors < 2 || cfg.GIFColors > 256 {
		return Config{}, errors.New("GIF colors must be between 2 and 256")
	}
	return cfg, nil
}

func rawEncodeArgs(cfg Config, output string) []string {
	overwrite := cfg.Overwrite || cfg.Format == FormatGIF
	inputWidth, inputHeight := inputDimensions(cfg)
	videoFilter := "pad=ceil(iw/2)*2:ceil(ih/2)*2"
	if cfg.CellColumns > 0 {
		if cfg.Format == FormatGIF {
			// Keep the live lossless stage at the canonical 2C-by-2R cell
			// raster. The identical nearest-neighbor projection is deferred to
			// the final palette pass, avoiding a full-canvas FFV1 encode while
			// the terminal is interactive.
			videoFilter = "null"
		} else {
			videoFilter = cellScaleFilter(cfg)
		}
	}
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-filter_threads", strconv.Itoa(ffmpegFilterThreads),
		overwriteFlag(overwrite),
		"-f", "rawvideo",
		"-pixel_format", "rgba",
		"-video_size", strconv.Itoa(inputWidth) + "x" + strconv.Itoa(inputHeight),
		"-framerate", strconv.Itoa(cfg.FPS),
		"-i", "pipe:0",
		"-an",
		"-vf", videoFilter,
	}
	if cfg.Format == FormatGIF {
		return append(args,
			"-c:v", "ffv1",
			"-threads:v", strconv.Itoa(ffmpegEncoderThreads),
			"-level", "3",
			"-pix_fmt", "bgra",
			"-f", "matroska",
			output,
		)
	}
	return append(args,
		"-c:v", "libx264",
		"-threads:v", strconv.Itoa(ffmpegEncoderThreads),
		"-preset", "veryfast",
		"-tune", "animation",
		"-crf", strconv.Itoa(cfg.CRF),
		"-pix_fmt", "yuv420p",
		output,
	)
}

func gifConvertArgs(cfg Config, input, output string) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "error",
		"-filter_threads", strconv.Itoa(ffmpegFilterThreads),
		"-filter_complex_threads", strconv.Itoa(ffmpegFilterThreads),
		"-y",
		"-i", input,
		"-filter_complex", gifFilter(cfg),
		"-threads:v", strconv.Itoa(ffmpegEncoderThreads),
		"-loop", "0",
		output,
	}
}

func cellScaleFilter(cfg Config) string {
	contentWidth, contentHeight, x, y, _ := cellCanvasGeometry(cfg.CellColumns, cfg.CellRows, cfg.Width, cfg.Height)
	return fmt.Sprintf(
		"scale=%d:%d:flags=neighbor,pad=%d:%d:%d:%d:color=black",
		contentWidth, contentHeight, cfg.Width, cfg.Height, x, y,
	)
}

func inputDimensions(cfg Config) (int, int) {
	if cfg.CellColumns > 0 && cfg.CellRows > 0 {
		return cfg.CellColumns * 2, cfg.CellRows * 2
	}
	return cfg.Width, cfg.Height
}

func cellCanvasGeometry(columns, rows, width, height int) (contentWidth, contentHeight, x, y int, err error) {
	if columns <= 0 || rows <= 0 || width <= 0 || height <= 0 {
		return 0, 0, 0, 0, errors.New("recording cell and canvas dimensions must be positive")
	}
	cellWidth := min(width/columns, height/(rows*2))
	if cellWidth < 1 {
		return 0, 0, 0, 0, errors.New("recording canvas cannot represent every terminal cell")
	}
	contentWidth, contentHeight = columns*cellWidth, rows*cellWidth*2
	return contentWidth, contentHeight, (width - contentWidth) / 2, (height - contentHeight) / 2, nil
}

func gifFilter(cfg Config) string {
	prefix := ""
	if cfg.CellColumns > 0 && cfg.CellRows > 0 {
		prefix = cellScaleFilter(cfg) + ","
	}
	return fmt.Sprintf(
		prefix+"fps=%d,split[s0][s1];[s0]palettegen=max_colors=%d:reserve_transparent=0:stats_mode=diff[p];[s1][p]paletteuse=dither=sierra2_4a:diff_mode=rectangle",
		cfg.FPS,
		cfg.GIFColors,
	)
}

func overwriteFlag(overwrite bool) string {
	if overwrite {
		return "-y"
	}
	return "-n"
}

func writeAll(destination io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := destination.Write(data)
		if err != nil {
			return fmt.Errorf("write FFmpeg frame: %w", err)
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func ffmpegError(action string, err error, stderr string) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, detail)
}
