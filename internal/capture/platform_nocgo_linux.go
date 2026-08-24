//go:build linux && !cgo

package capture

const (
	linuxMJPEG          = uint32(0)
	linuxFormatEmulated = uint32(1)
)

type linuxNativeMode struct {
	Mode
	bufferType uint32
	formatFlag uint32
	ordinal    uint32
}

type linuxNativeSample struct {
	bytesUsed    int
	index        uint32
	sequence     uint32
	seconds      int64
	microseconds int64
	monotonic    bool
	damaged      bool
}

type linuxNativeStream struct{}

type linuxJPEGLayout struct {
	imageWidth, imageHeight int
	yWidth, yHeight         int
	cbWidth, cbHeight       int
	crWidth, crHeight       int
}

type linuxJPEGDecoder struct{}

func linuxNativeAvailable() bool { return false }

func nativeProbe(string) (int, string, uint32, error) {
	return -1, "", 0, errLinuxCapturePrerequisite
}

func nativeCloseFD(int) {}

func nativeEnumerateModes(int, uint32, Config) ([]linuxNativeMode, error) {
	return nil, errLinuxCapturePrerequisite
}

func nativeConfigure(int, linuxNativeMode) (linuxNativeMode, error) {
	return linuxNativeMode{}, errLinuxCapturePrerequisite
}

func nativeStart(string, linuxNativeMode, int, int, int) (*linuxNativeStream, linuxNativeMode, error) {
	return nil, linuxNativeMode{}, errLinuxCapturePrerequisite
}

func (*linuxNativeStream) next(int) (linuxNativeSample, int, error) {
	return linuxNativeSample{}, 0, errLinuxCapturePrerequisite
}

func (*linuxNativeStream) copySample(linuxNativeSample, []byte) {}

func (*linuxNativeStream) requeue(uint32) error { return errLinuxCapturePrerequisite }

func (*linuxNativeStream) wake() error { return errLinuxCapturePrerequisite }

func (*linuxNativeStream) close() error { return nil }

func newLinuxJPEGDecoder() (*linuxJPEGDecoder, error) {
	return nil, errLinuxCapturePrerequisite
}

func (*linuxJPEGDecoder) layout([]byte, Mode, int) (linuxJPEGLayout, error) {
	return linuxJPEGLayout{}, errLinuxCapturePrerequisite
}

func (*linuxJPEGDecoder) decode([]byte, linuxJPEGLayout, *linuxPlaneBuffers) error {
	return errLinuxCapturePrerequisite
}

func (*linuxJPEGDecoder) close() {
}
