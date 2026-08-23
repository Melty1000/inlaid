//go:build windows

package mfcapture

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type wicPlaneDescription struct {
	Format windows.GUID
	Width  uint32
	Height uint32
}

type wicBitmapPlane struct {
	Format     windows.GUID
	Buffer     unsafe.Pointer
	Stride     uint32
	BufferSize uint32
}

type planeSpec struct {
	format                      windows.GUID
	width, height, stride, size int
}

type frameLayout [3]planeSpec
type pooledBuffers [3][]byte

type planarPool struct {
	cfg        Config
	mu         sync.Mutex
	layout     frameLayout
	configured bool
	free       chan *pooledBuffers
}

func (p *planarPool) acquire(ctx context.Context, stop <-chan struct{}, layout frameLayout) (*pooledBuffers, error) {
	p.mu.Lock()
	if !p.configured {
		frameBytes := 0
		for _, plane := range layout {
			if plane.width < 1 || plane.height < 1 || plane.stride < plane.width || plane.size != plane.stride*plane.height {
				p.mu.Unlock()
				return nil, errors.New("invalid WIC plane layout")
			}
			frameBytes += plane.size
		}
		if frameBytes > p.cfg.MaxFrameBytes {
			p.mu.Unlock()
			return nil, fmt.Errorf("WIC planar frame requires %d bytes, bound is %d", frameBytes, p.cfg.MaxFrameBytes)
		}
		slots := p.cfg.QueueDepth + 2
		if maximum := p.cfg.MaxPoolBytes / frameBytes; slots > maximum {
			slots = maximum
		}
		if slots < 1 {
			p.mu.Unlock()
			return nil, fmt.Errorf("WIC planar frame requires %d bytes but pool bound is %d", frameBytes, p.cfg.MaxPoolBytes)
		}
		p.layout = layout
		p.free = make(chan *pooledBuffers, slots)
		for slot := 0; slot < slots; slot++ {
			buffers := &pooledBuffers{}
			for index, plane := range layout {
				buffers[index] = make([]byte, plane.size)
			}
			p.free <- buffers
		}
		p.configured = true
	} else if p.layout != layout {
		p.mu.Unlock()
		return nil, errors.New("WIC plane layout changed during native MJPEG stream")
	}
	free := p.free
	p.mu.Unlock()
	select {
	case buffers := <-free:
		return buffers, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-stop:
		return nil, errors.New("WIC decoder is closing")
	}
}

func (p *planarPool) release(buffers *pooledBuffers) {
	if buffers == nil {
		return
	}
	p.mu.Lock()
	free := p.free
	p.mu.Unlock()
	if free != nil {
		free <- buffers
	}
}

type decodeRequest struct {
	ctx    context.Context
	packet Packet
}

type decodeResponse struct {
	frame *Frame
	err   error
}

type wicDecoder struct {
	cfg           Config
	pool          planarPool
	requests      chan decodeRequest
	responses     chan decodeResponse
	stop          chan struct{}
	done          chan struct{}
	closeOnce     sync.Once
	decodeMu      sync.Mutex
	closeMu       sync.Mutex
	closeErr      error
	decodeCount   atomic.Uint64
	decodeTotalNS atomic.Uint64
	decodeMaxNS   atomic.Uint64
}

func newWICDecoder(cfg Config) (*wicDecoder, error) {
	decoder := &wicDecoder{
		cfg: cfg, requests: make(chan decodeRequest), responses: make(chan decodeResponse),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	decoder.pool.cfg = cfg
	ready := make(chan error, 1)
	go decoder.control(ready)
	if err := <-ready; err != nil {
		<-decoder.done
		return nil, err
	}
	return decoder, nil
}

func (d *wicDecoder) Decode(ctx context.Context, packet Packet) (*Frame, error) {
	// The capture pipeline decodes serially. Keep that contract explicit so one
	// reusable response path replaces a heap channel allocation on every frame.
	d.decodeMu.Lock()
	defer d.decodeMu.Unlock()
	request := decodeRequest{ctx: ctx, packet: packet}
	select {
	case d.requests <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.done:
		return nil, errors.New("WIC decoder is closed")
	}
	// Once control accepted the request it may still be inside a synchronous WIC
	// call when ctx is canceled. Wait for its response or shutdown so the caller
	// cannot return pooled Packet.Data while WIC is still reading it.
	select {
	case response := <-d.responses:
		return response.frame, response.err
	case <-d.done:
		return nil, errors.New("WIC decoder closed before responding")
	}
}

func (d *wicDecoder) Close() error {
	d.closeOnce.Do(func() { close(d.stop) })
	<-d.done
	d.closeMu.Lock()
	defer d.closeMu.Unlock()
	return d.closeErr
}

func (d *wicDecoder) control(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(d.done)
	factory, shutdown, err := startWIC()
	if err != nil {
		ready <- err
		return
	}
	defer shutdown()
	ready <- nil
	for {
		select {
		case <-d.stop:
			return
		case request := <-d.requests:
			started := time.Now()
			frame, decodeErr := d.decodePacket(request.ctx, factory, request.packet)
			elapsed := uint64(time.Since(started))
			d.decodeCount.Add(1)
			d.decodeTotalNS.Add(elapsed)
			atomicMax(&d.decodeMaxNS, elapsed)
			response := decodeResponse{frame: frame, err: decodeErr}
			select {
			case d.responses <- response:
			case <-d.stop:
				if frame != nil {
					frame.Release()
				}
				return
			}
		}
	}
}

func (d *wicDecoder) decodePacket(ctx context.Context, factory comObject, packet Packet) (*Frame, error) {
	if len(packet.Data) < 4 || len(packet.Data) > d.cfg.MaxPacketBytes {
		return nil, fmt.Errorf("MJPEG length %d is outside 4..%d", len(packet.Data), d.cfg.MaxPacketBytes)
	}
	if packet.Data[0] != 0xff || packet.Data[1] != 0xd8 || packet.Data[len(packet.Data)-2] != 0xff || packet.Data[len(packet.Data)-1] != 0xd9 {
		return nil, errors.New("MJPEG packet is not one complete JPEG")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var streamPtr unsafe.Pointer
	hr := factory.call(14, uintptr(unsafe.Pointer(&streamPtr)))
	if failed(hr) || streamPtr == nil {
		return nil, hrError("IWICImagingFactory.CreateStream", hr)
	}
	stream := comObject{streamPtr}
	defer stream.release()
	if hr = stream.call(16, uintptr(unsafe.Pointer(&packet.Data[0])), uintptr(len(packet.Data))); failed(hr) {
		return nil, hrError("IWICStream.InitializeFromMemory", hr)
	}

	var decoderPtr unsafe.Pointer
	hr, _, _ = procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidWICJpegDecoder)), 0, clsctxInprocServer, uintptr(unsafe.Pointer(&iidIWICBitmapDecoder)), uintptr(unsafe.Pointer(&decoderPtr)))
	if failed(hr) || decoderPtr == nil {
		return nil, hrError("CoCreateInstance(CLSID_WICJpegDecoder)", hr)
	}
	decoder := comObject{decoderPtr}
	defer decoder.release()
	if hr = decoder.call(4, uintptr(stream.ptr), wicDecodeMetadataCacheOnDemand); failed(hr) {
		return nil, hrError("IWICBitmapDecoder.Initialize", hr)
	}
	var count uint32
	if hr = decoder.call(12, uintptr(unsafe.Pointer(&count))); failed(hr) {
		return nil, hrError("IWICBitmapDecoder.GetFrameCount", hr)
	}
	if count != 1 {
		return nil, fmt.Errorf("WIC JPEG frame count is %d, want 1", count)
	}
	var framePtr unsafe.Pointer
	if hr = decoder.call(13, 0, uintptr(unsafe.Pointer(&framePtr))); failed(hr) || framePtr == nil {
		return nil, hrError("IWICBitmapDecoder.GetFrame(0)", hr)
	}
	frameSource := comObject{framePtr}
	defer frameSource.release()
	var sourceWidth, sourceHeight uint32
	if hr = frameSource.call(3, uintptr(unsafe.Pointer(&sourceWidth)), uintptr(unsafe.Pointer(&sourceHeight))); failed(hr) {
		return nil, hrError("IWICBitmapSource.GetSize", hr)
	}
	if int(sourceWidth) != d.cfg.Width || int(sourceHeight) != d.cfg.Height {
		return nil, fmt.Errorf("WIC JPEG source is %dx%d, want %dx%d", sourceWidth, sourceHeight, d.cfg.Width, d.cfg.Height)
	}
	var planarPtr unsafe.Pointer
	if hr = frameSource.call(0, uintptr(unsafe.Pointer(&iidIWICPlanarBitmapSourceTransform)), uintptr(unsafe.Pointer(&planarPtr))); failed(hr) || planarPtr == nil {
		return nil, hrError("QueryInterface(IWICPlanarBitmapSourceTransform)", hr)
	}
	planar := comObject{planarPtr}
	defer planar.release()

	width := uint32(reducedDimension(d.cfg.Width, d.cfg.Lowres))
	height := uint32(reducedDimension(d.cfg.Height, d.cfg.Lowres))
	requestedWidth, requestedHeight := width, height
	formats := [3]windows.GUID{wicPixelFormatY, wicPixelFormatCb, wicPixelFormatCr}
	var descriptions [3]wicPlaneDescription
	var supported int32
	hr = planar.call(3, uintptr(unsafe.Pointer(&width)), uintptr(unsafe.Pointer(&height)), wicBitmapTransformRotate0, wicPreserveSubsampling, uintptr(unsafe.Pointer(&formats[0])), uintptr(unsafe.Pointer(&descriptions[0])), 3, uintptr(unsafe.Pointer(&supported)))
	if failed(hr) {
		return nil, hrError("IWICPlanarBitmapSourceTransform.DoesSupportTransform", hr)
	}
	if supported == 0 {
		return nil, fmt.Errorf("built-in WIC JPEG decoder does not support planar lowres=%d", d.cfg.Lowres)
	}
	if width != requestedWidth || height != requestedHeight {
		return nil, fmt.Errorf("WIC reduced geometry is %dx%d, want %dx%d", width, height, requestedWidth, requestedHeight)
	}
	if descriptions[0].Format != wicPixelFormatY || descriptions[1].Format != wicPixelFormatCb || descriptions[2].Format != wicPixelFormatCr {
		return nil, errors.New("WIC did not preserve Y/Cb/Cr planar formats")
	}
	if descriptions[1].Width != descriptions[2].Width || descriptions[1].Height != descriptions[2].Height || descriptions[1].Width > descriptions[0].Width || descriptions[1].Height > descriptions[0].Height {
		return nil, errors.New("WIC did not preserve valid chroma subsampling")
	}
	var layout frameLayout
	for index, description := range descriptions {
		if description.Width == 0 || description.Height == 0 || description.Width > maxDimension || description.Height > maxDimension {
			return nil, fmt.Errorf("WIC plane %d dimensions are invalid: %dx%d", index, description.Width, description.Height)
		}
		size64 := int64(description.Width) * int64(description.Height)
		if size64 > int64(d.cfg.MaxFrameBytes) {
			return nil, fmt.Errorf("WIC plane %d requires %d bytes", index, size64)
		}
		layout[index] = planeSpec{format: formats[index], width: int(description.Width), height: int(description.Height), stride: int(description.Width), size: int(size64)}
	}
	buffers, err := d.pool.acquire(ctx, d.stop, layout)
	if err != nil {
		return nil, err
	}
	releaseBuffers := true
	defer func() {
		if releaseBuffers {
			d.pool.release(buffers)
		}
	}()
	planes := [3]wicBitmapPlane{}
	for index, spec := range layout {
		planes[index] = wicBitmapPlane{Format: spec.format, Buffer: unsafe.Pointer(&buffers[index][0]), Stride: uint32(spec.stride), BufferSize: uint32(spec.size)}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hr = planar.call(4, 0, uintptr(width), uintptr(height), wicBitmapTransformRotate0, wicPreserveSubsampling, uintptr(unsafe.Pointer(&planes[0])), 3)
	if failed(hr) {
		return nil, hrError("IWICPlanarBitmapSourceTransform.CopyPixels", hr)
	}
	runtime.KeepAlive(packet.Data)
	runtime.KeepAlive(buffers)
	result := &Frame{
		Y:                    Plane{Pix: buffers[0], Width: layout[0].width, Height: layout[0].height, Stride: layout[0].stride},
		Cb:                   Plane{Pix: buffers[1], Width: layout[1].width, Height: layout[1].height, Stride: layout[1].stride},
		Cr:                   Plane{Pix: buffers[2], Width: layout[2].width, Height: layout[2].height, Stride: layout[2].stride},
		ReaderTimestamp100ns: packet.ReaderTimestamp100ns,
		SampleTimestamp100ns: packet.SampleTimestamp100ns,
		SampleDuration100ns:  packet.SampleDuration100ns,
		DurationKnown:        packet.DurationKnown,
		ReaderFlags:          packet.ReaderFlags, SampleFlags: packet.SampleFlags, SourceBufferCount: packet.BufferCount,
		release: func() { d.pool.release(buffers) },
	}
	releaseBuffers = false
	return result, nil
}
