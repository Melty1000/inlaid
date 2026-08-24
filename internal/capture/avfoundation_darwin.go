//go:build darwin && cgo

package capture

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc -fmodules -fblocks
#cgo darwin LDFLAGS: -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework Foundation
#cgo darwin LDFLAGS: -Wl,-sectcreate,__TEXT,__info_plist,${SRCDIR}/Info_darwin.plist

#include <stdint.h>
#include <stdlib.h>
#include "avfoundation_bridge_darwin.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime/cgo"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

type avfModeRange struct {
	FormatIndex      int    `json:"format_index"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	Format           string `json:"format"`
	Subtype          uint32 `json:"subtype"`
	MinimumValue     int64  `json:"minimum_value"`
	MinimumTimescale int32  `json:"minimum_timescale"`
	MaximumValue     int64  `json:"maximum_value"`
	MaximumTimescale int32  `json:"maximum_timescale"`
}

type avfModeChoice struct {
	modeCandidate
	formatIndex            int
	sourceSubtype          uint32
	frameDurationValue     int64
	frameDurationTimescale int32
}

type avfDelivery struct {
	session        atomic.Pointer[Session]
	fatal          chan error
	framePulse     chan struct{}
	expectedWidth  int
	expectedHeight int
	maxFrameBytes  int64
	maxPoolBytes   int64
	retainedBytes  atomic.Int64
	sequence       atomic.Uint64
	ptsMu          sync.Mutex
	hasFirstPTS    bool
	firstPTS       time.Duration
	badFormatOnce  sync.Once
	badMatrixOnce  sync.Once
}

type avfNativeCapture struct {
	pointer unsafe.Pointer
	handle  cgo.Handle
}

func Enumerate(ctx context.Context) ([]Device, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := avfAuthorize(ctx); err != nil {
		return nil, err
	}
	var nativeError *C.char
	encoded := C.inlaid_avf_devices_json(&nativeError)
	if nativeError != nil {
		defer C.inlaid_avf_free_string(nativeError)
		return nil, errors.New(C.GoString(nativeError))
	}
	if encoded == nil {
		return nil, errors.New("AVFoundation returned no camera inventory")
	}
	defer C.inlaid_avf_free_string(encoded)
	var devices []Device
	if err := json.Unmarshal([]byte(C.GoString(encoded)), &devices); err != nil {
		return nil, fmt.Errorf("decode AVFoundation camera inventory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return devices, nil
}

func Open(parent context.Context, cfg Config) (*Session, error) {
	if parent == nil {
		parent = context.Background()
	}
	normalized, err := normalize(cfg, true)
	if err != nil {
		return nil, err
	}
	if err := avfAuthorize(parent); err != nil {
		return nil, err
	}
	ranges, err := avfModeRanges(normalized.DeviceID)
	if err != nil {
		return nil, err
	}
	choice, ok := chooseAVFMode(ranges, normalized)
	if !ok {
		return nil, fmt.Errorf("camera %q exposes no usable AVFoundation video mode", normalized.DeviceID)
	}
	outputWidth, outputHeight := avfOutputDimensions(choice.Mode.Width, choice.Mode.Height, normalized.Downsample)
	if outputWidth < 2 || outputHeight < 2 {
		return nil, errors.New("selected AVFoundation mode cannot produce a reduced NV12 frame")
	}
	if outputWidth&1 != 0 || outputHeight&1 != 0 {
		return nil, fmt.Errorf("selected AVFoundation mode cannot preserve its aspect ratio in an even NV12 reduction: %dx%d", outputWidth, outputHeight)
	}
	if int64(outputWidth)*int64(outputHeight)*3/2 > int64(normalized.MaxFrameBytes) {
		return nil, fmt.Errorf("reduced AVFoundation NV12 frame exceeds %d-byte bound", normalized.MaxFrameBytes)
	}
	delivery := &avfDelivery{
		fatal:          make(chan error, 1),
		framePulse:     make(chan struct{}, 1),
		expectedWidth:  outputWidth,
		expectedHeight: outputHeight,
		maxFrameBytes:  int64(normalized.MaxFrameBytes),
		maxPoolBytes:   int64(normalized.MaxPoolBytes),
	}
	native, err := newAVFNativeCapture(normalized, choice, outputWidth, outputHeight, delivery)
	if err != nil {
		return nil, err
	}
	ready := make(chan error, 1)
	session, err := startDirect(parent, normalized, choice.Mode, func(ctx context.Context, session *Session) error {
		delivery.session.Store(session)
		if err := ctx.Err(); err != nil {
			ready <- err
			closeErr, safe := native.close(normalized.CloseTimeout)
			if safe {
				native.handle.Delete()
			}
			return closeErr
		}
		startErr := native.start()
		ready <- startErr
		if startErr != nil {
			closeErr, safe := native.close(normalized.CloseTimeout)
			if safe {
				native.handle.Delete()
			}
			return errors.Join(startErr, closeErr)
		}
		watchdog := time.NewTimer(frameWatchdogTimeout(normalized.FPS, true))
		var runErr error
	waitForCapture:
		for {
			select {
			case <-ctx.Done():
				break waitForCapture
			case runErr = <-delivery.fatal:
				break waitForCapture
			case <-delivery.framePulse:
				resetTimer(watchdog, frameWatchdogTimeout(normalized.FPS, false))
			case <-watchdog.C:
				runErr = fmt.Errorf("no AVFoundation camera frame arrived for %s", frameWatchdogTimeout(normalized.FPS, delivery.sequence.Load() == 0))
				break waitForCapture
			}
		}
		if !watchdog.Stop() {
			select {
			case <-watchdog.C:
			default:
			}
		}
		closeErr, safe := native.close(normalized.CloseTimeout)
		if safe {
			native.handle.Delete()
		}
		return errors.Join(runErr, closeErr)
	})
	if err != nil {
		closeErr, safe := native.close(normalized.CloseTimeout)
		if safe {
			native.handle.Delete()
		}
		return nil, errors.Join(err, closeErr)
	}
	select {
	case startErr := <-ready:
		if startErr != nil {
			return nil, errors.Join(startErr, session.Close())
		}
		if err := parent.Err(); err != nil {
			return nil, errors.Join(err, session.Close())
		}
		return session, nil
	case <-parent.Done():
		return nil, errors.Join(parent.Err(), session.Close())
	}
}

func avfAuthorize(ctx context.Context) error {
	result := make(chan error, 1)
	go func() {
		message := C.inlaid_avf_authorize()
		if message == nil {
			result <- nil
			return
		}
		defer C.inlaid_avf_free_string(message)
		result <- errors.New(C.GoString(message))
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func avfModeRanges(deviceID string) ([]avfModeRange, error) {
	id := C.CString(deviceID)
	defer C.free(unsafe.Pointer(id))
	var nativeError *C.char
	encoded := C.inlaid_avf_modes_json(id, &nativeError)
	if nativeError != nil {
		defer C.inlaid_avf_free_string(nativeError)
		return nil, errors.New(C.GoString(nativeError))
	}
	if encoded == nil {
		return nil, errors.New("AVFoundation returned no camera mode inventory")
	}
	defer C.inlaid_avf_free_string(encoded)
	var ranges []avfModeRange
	if err := json.Unmarshal([]byte(C.GoString(encoded)), &ranges); err != nil {
		return nil, fmt.Errorf("decode AVFoundation camera modes: %w", err)
	}
	return ranges, nil
}

func chooseAVFMode(ranges []avfModeRange, cfg Config) (avfModeChoice, bool) {
	target := Mode{
		Width: cfg.Width, Height: cfg.Height,
		FPSNumerator: uint32(cfg.FPS), FPSDenominator: 1,
		Format: "AVF",
	}
	choices := make([]avfModeChoice, 0, len(ranges))
	candidates := make([]modeCandidate, 0, len(ranges))
	for sourceIndex, nativeRange := range ranges {
		value, timescale, ok := avfClampedFrameDuration(nativeRange, int64(cfg.FPS))
		if !ok || nativeRange.FormatIndex < 0 {
			continue
		}
		numerator, denominator, ok := avfRate(value, timescale)
		if !ok {
			continue
		}
		format := strings.TrimSpace(nativeRange.Format)
		mode := Mode{
			Width: nativeRange.Width, Height: nativeRange.Height,
			FPSNumerator: numerator, FPSDenominator: denominator,
			Format: format,
		}
		stableIndex := avfFormatPreference(format)*1_000_000 + sourceIndex
		candidate := modeCandidate{Mode: mode, Index: stableIndex}
		choices = append(choices, avfModeChoice{
			modeCandidate:          candidate,
			formatIndex:            nativeRange.FormatIndex,
			sourceSubtype:          nativeRange.Subtype,
			frameDurationValue:     value,
			frameDurationTimescale: timescale,
		})
		candidates = append(candidates, candidate)
	}
	best, ok := chooseBestMode(candidates, target)
	if !ok {
		return avfModeChoice{}, false
	}
	for _, choice := range choices {
		if choice.modeCandidate == best {
			return choice, true
		}
	}
	return avfModeChoice{}, false
}

func avfClampedFrameDuration(mode avfModeRange, requestedFPS int64) (int64, int32, bool) {
	if requestedFPS < 1 || !validAVFTime(mode.MinimumValue, mode.MinimumTimescale) || !validAVFTime(mode.MaximumValue, mode.MaximumTimescale) {
		return 0, 0, false
	}
	requestedValue, requestedTimescale := int64(1), int32(requestedFPS)
	if compareAVFTime(requestedValue, requestedTimescale, mode.MinimumValue, mode.MinimumTimescale) < 0 {
		return mode.MinimumValue, mode.MinimumTimescale, true
	}
	if compareAVFTime(requestedValue, requestedTimescale, mode.MaximumValue, mode.MaximumTimescale) > 0 {
		return mode.MaximumValue, mode.MaximumTimescale, true
	}
	return requestedValue, requestedTimescale, true
}

func validAVFTime(value int64, timescale int32) bool {
	return value > 0 && timescale > 0
}

func compareAVFTime(leftValue int64, leftScale int32, rightValue int64, rightScale int32) int {
	return compareFractions(uint64(leftValue), uint64(leftScale), uint64(rightValue), uint64(rightScale))
}

func avfRate(durationValue int64, durationTimescale int32) (uint32, uint32, bool) {
	if !validAVFTime(durationValue, durationTimescale) {
		return 0, 0, false
	}
	divisor := gcd64(uint64(durationValue), uint64(durationTimescale))
	numerator := uint64(durationTimescale) / divisor
	denominator := uint64(durationValue) / divisor
	if numerator == 0 || denominator == 0 || numerator > math.MaxUint32 || denominator > math.MaxUint32 {
		return 0, 0, false
	}
	return uint32(numerator), uint32(denominator), true
}

func avfFormatPreference(format string) int {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "mjpg", "jpeg", "dmb1":
		return 0
	case "420v", "420f", "2vuy", "yuvs":
		return 1
	default:
		return 2
	}
}

func avfOutputDimensions(width, height, downsample int) (int, int) {
	if width < 1 || height < 1 || downsample < 1 {
		return 0, 0
	}
	divisor := int(gcd64(uint64(width), uint64(height)))
	if divisor < 1 {
		return 0, 0
	}
	scale := divisor / downsample
	if scale < 1 {
		scale = 1
	}
	outputWidth := width / divisor * scale
	outputHeight := height / divisor * scale
	if outputWidth&1 != 0 || outputHeight&1 != 0 {
		for scale > 1 && (outputWidth&1 != 0 || outputHeight&1 != 0) {
			scale--
			outputWidth = width / divisor * scale
			outputHeight = height / divisor * scale
		}
	}
	return outputWidth, outputHeight
}

func gcd64(left, right uint64) uint64 {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}

func newAVFNativeCapture(cfg Config, choice avfModeChoice, outputWidth, outputHeight int, delivery *avfDelivery) (*avfNativeCapture, error) {
	handle := cgo.NewHandle(delivery)
	id := C.CString(cfg.DeviceID)
	defer C.free(unsafe.Pointer(id))
	var nativeError *C.char
	pointer := C.inlaid_avf_create(
		id,
		C.int(choice.formatIndex),
		C.uint32_t(choice.sourceSubtype),
		C.int(choice.Mode.Width),
		C.int(choice.Mode.Height),
		C.int64_t(choice.frameDurationValue),
		C.int32_t(choice.frameDurationTimescale),
		C.int(outputWidth),
		C.int(outputHeight),
		C.int(boolInt(cfg.AllowVariableFrameRate)),
		C.uintptr_t(handle),
		&nativeError,
	)
	if nativeError != nil {
		defer C.inlaid_avf_free_string(nativeError)
		handle.Delete()
		return nil, errors.New(C.GoString(nativeError))
	}
	if pointer == nil {
		handle.Delete()
		return nil, errors.New("AVFoundation did not create a capture session")
	}
	return &avfNativeCapture{pointer: pointer, handle: handle}, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (capture *avfNativeCapture) start() error {
	if capture == nil || capture.pointer == nil {
		return errors.New("AVFoundation capture session is unavailable")
	}
	message := C.inlaid_avf_start(capture.pointer)
	if message == nil {
		return nil
	}
	defer C.inlaid_avf_free_string(message)
	return errors.New(C.GoString(message))
}

func (capture *avfNativeCapture) close(timeout time.Duration) (error, bool) {
	if capture == nil || capture.pointer == nil {
		return nil, true
	}
	var nativeError *C.char
	result := C.inlaid_avf_close(capture.pointer, C.int64_t(timeout/time.Millisecond), &nativeError)
	if result == 0 {
		capture.pointer = nil
	}
	var err error
	if nativeError != nil {
		err = errors.New(C.GoString(nativeError))
		C.inlaid_avf_free_string(nativeError)
	}
	if result != 0 {
		if err == nil {
			err = fmt.Errorf("%w: AVFoundation shutdown exceeded %s", ErrShutdownUncertain, timeout)
		} else {
			err = errors.Join(ErrShutdownUncertain, err)
		}
		return err, false
	}
	return err, true
}

//export inlaidAVFDeliver
func inlaidAVFDeliver(
	handle C.uintptr_t,
	frameReference unsafe.Pointer,
	yAddress unsafe.Pointer,
	yBytes C.size_t,
	uvAddress unsafe.Pointer,
	uvBytes C.size_t,
	width C.int,
	height C.int,
	yStride C.size_t,
	uvStride C.size_t,
	pixelFormat C.uint32_t,
	matrix C.int,
	ptsValue C.int64_t,
	ptsTimescale C.int32_t,
) (accepted C.int) {
	var reservation int64
	var reservationOwner *avfDelivery
	defer func() {
		if recover() != nil {
			accepted = 0
		}
		if accepted == 0 && reservation != 0 && reservationOwner != nil {
			reservationOwner.retainedBytes.Add(-reservation)
		}
	}()
	delivery, ok := cgo.Handle(handle).Value().(*avfDelivery)
	if !ok || delivery == nil || frameReference == nil {
		return 0
	}
	session := delivery.session.Load()
	if session == nil {
		return 0
	}
	session.stats.packets.Add(1)
	frameWidth, frameHeight := int(width), int(height)
	yStrideValue, uvStrideValue := uint64(yStride), uint64(uvStride)
	yLengthValue, uvLengthValue := uint64(yBytes), uint64(uvBytes)
	uvHeight := uint64((frameHeight + 1) / 2)
	if frameWidth != delivery.expectedWidth || frameHeight != delivery.expectedHeight ||
		frameWidth < 2 || frameHeight < 2 || yAddress == nil || uvAddress == nil ||
		yStrideValue < uint64(frameWidth) || uvStrideValue < uint64(((frameWidth+1)/2)*2) ||
		yStrideValue > uint64(math.MaxInt) || uvStrideValue > uint64(math.MaxInt) ||
		yLengthValue > uint64(math.MaxInt) || uvLengthValue > uint64(math.MaxInt) ||
		yStrideValue > math.MaxUint64/uint64(frameHeight) || uvStrideValue > math.MaxUint64/uvHeight ||
		yLengthValue < yStrideValue*uint64(frameHeight) || uvLengthValue < uvStrideValue*uvHeight {
		delivery.failOnce(&delivery.badFormatOnce, fmt.Errorf(
			"AVFoundation delivered an unexpected NV12 buffer: got %dx%d, want %dx%d",
			frameWidth, frameHeight, delivery.expectedWidth, delivery.expectedHeight,
		))
		return 0
	}
	colorRange := ColorRange(0)
	switch uint32(pixelFormat) {
	case 0x34323066:
		colorRange = ColorRangeFull
	case 0x34323076:
		colorRange = ColorRangeVideo
	default:
		delivery.failOnce(&delivery.badFormatOnce, fmt.Errorf("AVFoundation delivered unsupported pixel format 0x%08x", uint32(pixelFormat)))
		return 0
	}
	colorMatrix := ColorMatrix(0)
	switch int(matrix) {
	case 601:
		colorMatrix = ColorMatrixBT601
	case 709:
		colorMatrix = ColorMatrixBT709
	default:
		delivery.failOnce(&delivery.badMatrixOnce, errors.New("AVFoundation delivered NV12 without BT.601 or BT.709 matrix metadata"))
		return 0
	}
	pts, ok := avfDuration(int64(ptsValue), int32(ptsTimescale))
	if !ok {
		delivery.failOnce(&delivery.badFormatOnce, errors.New("AVFoundation delivered a frame without a numeric presentation timestamp"))
		return 0
	}
	delivery.ptsMu.Lock()
	if !delivery.hasFirstPTS {
		delivery.firstPTS = pts
		delivery.hasFirstPTS = true
	}
	pts -= delivery.firstPTS
	if pts < 0 {
		pts = 0
	}
	delivery.ptsMu.Unlock()
	yLength, uvLength := int(yBytes), int(uvBytes)
	frameBytes := int64(yLength) + int64(uvLength)
	if frameBytes < 1 || frameBytes > delivery.maxFrameBytes {
		delivery.failOnce(&delivery.badFormatOnce, fmt.Errorf("AVFoundation NV12 frame requires %d bytes, bound is %d", frameBytes, delivery.maxFrameBytes))
		return 0
	}
	if !delivery.reserve(frameBytes) {
		session.stats.droppedFrames.Add(1)
		return 0
	}
	reservation = frameBytes
	reservationOwner = delivery
	frame := &Frame{
		Layout: PixelLayoutNV12,
		Range:  colorRange,
		Matrix: colorMatrix,
		Y: Plane{
			Pix:   unsafe.Slice((*byte)(yAddress), yLength),
			Width: frameWidth, Height: frameHeight, Stride: int(yStride),
		},
		UV: Plane{
			Pix:   unsafe.Slice((*byte)(uvAddress), uvLength),
			Width: ((frameWidth + 1) / 2) * 2, Height: (frameHeight + 1) / 2, Stride: int(uvStride),
		},
		PTS: pts,
		release: func() {
			delivery.retainedBytes.Add(-frameBytes)
			C.inlaid_avf_frame_release(frameReference)
		},
	}
	session.acceptFrame(frame)
	reservation = 0
	accepted = 1
	delivery.sequence.Store(frame.Sequence)
	select {
	case delivery.framePulse <- struct{}{}:
	default:
	}
	return accepted
}

//export inlaidAVFError
func inlaidAVFError(handle C.uintptr_t, message *C.char, temporary C.int) {
	defer func() { _ = recover() }()
	delivery, ok := cgo.Handle(handle).Value().(*avfDelivery)
	if !ok || delivery == nil || message == nil {
		return
	}
	err := errors.New(C.GoString(message))
	if temporary != 0 {
		if session := delivery.session.Load(); session != nil {
			session.report(temporaryCaptureError{err: err})
		}
		return
	}
	select {
	case delivery.fatal <- err:
	default:
	}
}

//export inlaidAVFDropped
func inlaidAVFDropped(handle C.uintptr_t) {
	defer func() { _ = recover() }()
	delivery, ok := cgo.Handle(handle).Value().(*avfDelivery)
	if !ok || delivery == nil {
		return
	}
	if session := delivery.session.Load(); session != nil {
		session.stats.droppedPackets.Add(1)
	}
}

func (delivery *avfDelivery) failOnce(once *sync.Once, err error) {
	once.Do(func() {
		select {
		case delivery.fatal <- err:
		default:
		}
	})
}

func (delivery *avfDelivery) reserve(size int64) bool {
	for {
		retained := delivery.retainedBytes.Load()
		if size > delivery.maxPoolBytes-retained {
			return false
		}
		if delivery.retainedBytes.CompareAndSwap(retained, retained+size) {
			return true
		}
	}
}

func avfDuration(value int64, timescale int32) (time.Duration, bool) {
	if timescale <= 0 {
		return 0, false
	}
	scale := int64(timescale)
	seconds, remainder := value/scale, value%scale
	if seconds > math.MaxInt64/int64(time.Second) || seconds < math.MinInt64/int64(time.Second) {
		return 0, false
	}
	base := seconds * int64(time.Second)
	fraction := remainder * int64(time.Second) / scale
	if fraction > 0 && base > math.MaxInt64-fraction || fraction < 0 && base < math.MinInt64-fraction {
		return 0, false
	}
	return time.Duration(base + fraction), true
}
