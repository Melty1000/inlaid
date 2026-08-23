//go:build windows

package mfcapture

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	mfVersion                          = 0x00020070
	mfStartupFull                      = 0
	mfSourceReaderAllStreams           = 0xfffffffe
	mfSourceReaderFirstVideoStream     = 0xfffffffc
	mfSourceReaderFlagError            = 0x1
	mfSourceReaderFlagEOS              = 0x2
	mfSourceReaderFlagStreamTick       = 0x100
	mfEventError                       = 1
	mfEventNonFatalError               = 3
	mfEventVideoCaptureDeviceRemoved   = 800
	mfEventVideoCaptureDevicePreempted = 801
	mfENoMoreTypes                     = 0xc00d36b9
	clsctxInprocServer                 = 0x1
	coinitMultithreaded                = 0x0
	wicDecodeMetadataCacheOnDemand     = 0
	wicBitmapTransformRotate0          = 0
	wicPreserveSubsampling             = 0x1
	maxDeviceCount                     = 128
	maxNativeTypes                     = 512
	hresultNoInterface                 = 0x80004002
	hresultPointer                     = 0x80004003
	cameraControlAutoExposurePriority  = 19
	cameraControlExposure              = 4
	cameraControlFlagAuto              = 0x1
	cameraControlFlagManual            = 0x2
	videoProcAmpGain                   = 9
)

var (
	mfplat      = windows.NewLazySystemDLL("mfplat.dll")
	mfdll       = windows.NewLazySystemDLL("mf.dll")
	mfreadwrite = windows.NewLazySystemDLL("mfreadwrite.dll")
	ole32       = windows.NewLazySystemDLL("ole32.dll")

	procMFStartup                           = mfplat.NewProc("MFStartup")
	procMFShutdown                          = mfplat.NewProc("MFShutdown")
	procMFCreateAttributes                  = mfplat.NewProc("MFCreateAttributes")
	procMFEnumDeviceSources                 = mfdll.NewProc("MFEnumDeviceSources")
	procMFCreateSourceReaderFromMediaSource = mfreadwrite.NewProc("MFCreateSourceReaderFromMediaSource")
	procCoInitializeEx                      = ole32.NewProc("CoInitializeEx")
	procCoUninitialize                      = ole32.NewProc("CoUninitialize")
	procCoCreateInstance                    = ole32.NewProc("CoCreateInstance")
)

var (
	iidIUnknown           = guid(0x00000000, 0x0000, 0x0000, 0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46)
	iidIAMCameraControl   = guid(0xc6e13370, 0x30ac, 0x11d0, 0xa1, 0x8c, 0x00, 0xa0, 0xc9, 0x11, 0x89, 0x56)
	iidIAMVideoProcAmp    = guid(0xc6e13360, 0x30ac, 0x11d0, 0xa1, 0x8c, 0x00, 0xa0, 0xc9, 0x11, 0x89, 0x56)
	iidIMFMediaSource     = guid(0x279a808d, 0xaec7, 0x40c8, 0x9c, 0x6b, 0xa6, 0xb4, 0x92, 0xc7, 0x8a, 0x66)
	iidIMFSourceReaderCB  = guid(0xdeec8d99, 0xfa1d, 0x4d82, 0x84, 0xc2, 0x2c, 0x89, 0x69, 0x94, 0x48, 0x67)
	mfDevSourceType       = guid(0xc60ac5fe, 0x252a, 0x478f, 0xa0, 0xef, 0xbc, 0x8f, 0xa5, 0xf7, 0xca, 0xd3)
	mfDevSourceVideo      = guid(0x8ac3587a, 0x4ae7, 0x42d8, 0x99, 0xe0, 0x0a, 0x60, 0x13, 0xee, 0xf9, 0x0f)
	mfFriendlyName        = guid(0x60d0e559, 0x52f8, 0x4fa2, 0xbb, 0xce, 0xac, 0xdb, 0x34, 0xa8, 0xec, 0x01)
	mfVideoSymbolicLink   = guid(0x58f0aad8, 0x22bf, 0x4f8a, 0xbb, 0x3d, 0xd2, 0xc4, 0x97, 0x8c, 0x6e, 0x2f)
	mfMTMajorType         = guid(0x48eba18e, 0xf8c9, 0x4687, 0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f)
	mfMTSubtype           = guid(0xf7e34c9a, 0x42e8, 0x4714, 0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5)
	mfMTFrameSize         = guid(0x1652c33d, 0xd6b2, 0x4012, 0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d)
	mfMTFrameRate         = guid(0xc459a2e8, 0x3d2c, 0x4e44, 0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0)
	mfMediaTypeVideo      = guid(0x73646976, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71)
	mfVideoFormatMJPG     = guid(0x47504a4d, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71)
	mfReaderAsyncCallback = guid(0x1e3dbeac, 0xbb43, 0x4c35, 0xb5, 0x07, 0xcd, 0x64, 0x44, 0x64, 0xc9, 0x65)
	mfReaderDisconnect    = guid(0x56b67165, 0x219e, 0x456d, 0xa2, 0x2e, 0x2d, 0x30, 0x04, 0xc7, 0xfe, 0x56)

	clsidWICImagingFactory             = guid(0xcacaf262, 0x9370, 0x4615, 0xa1, 0x3b, 0x9f, 0x55, 0x39, 0xda, 0x4c, 0x0a)
	iidIWICImagingFactory              = guid(0xec5ec8a9, 0xc395, 0x4314, 0x9c, 0x77, 0x54, 0xd7, 0xa9, 0x35, 0xff, 0x70)
	clsidWICJpegDecoder                = guid(0x9456a480, 0xe88b, 0x43ea, 0x9e, 0x73, 0x0b, 0x2d, 0x9b, 0x71, 0xb1, 0xca)
	iidIWICBitmapDecoder               = guid(0x9edde9e7, 0x8dee, 0x47ea, 0x99, 0xdf, 0xe6, 0xfa, 0xf2, 0xed, 0x44, 0xbf)
	iidIWICPlanarBitmapSourceTransform = guid(0x3aff9cce, 0xbe95, 0x4303, 0xb9, 0x27, 0xe7, 0xd1, 0x6f, 0xf4, 0xa6, 0x13)
	wicPixelFormatY                    = guid(0x91b4db54, 0x2df9, 0x42f0, 0xb4, 0x49, 0x29, 0x09, 0xbb, 0x3d, 0xf8, 0x8e)
	wicPixelFormatCb                   = guid(0x1339f224, 0x6bfe, 0x4c3e, 0x93, 0x02, 0xe4, 0xf3, 0xa6, 0xd0, 0xca, 0x2a)
	wicPixelFormatCr                   = guid(0xb8145053, 0x2116, 0x49f0, 0x88, 0x35, 0xed, 0x84, 0x4b, 0x20, 0x5c, 0x51)
)

type frameRateControlResult struct {
	Method                   string
	Minimum, Maximum         int32
	Step, Default, Caps      int32
	BeforeValue, BeforeFlags int32
	AfterValue, AfterFlags   int32
	Gain                     procAmpControlResult
	Applied                  bool
	Err                      error
}

type procAmpControlResult struct {
	Method                   string
	Minimum, Maximum         int32
	Step, Default, Caps      int32
	BeforeValue, BeforeFlags int32
	AfterValue, AfterFlags   int32
	Applied                  bool
	Err                      error
}

type comObject struct{ ptr unsafe.Pointer }

func (o comObject) valid() bool { return o.ptr != nil }

func (o comObject) method(index int) uintptr {
	vtbl := *(*unsafe.Pointer)(o.ptr)
	return *(*uintptr)(unsafe.Add(vtbl, uintptr(index)*unsafe.Sizeof(uintptr(0))))
}

func (o comObject) call(index int, args ...uintptr) uintptr {
	all := make([]uintptr, 1, len(args)+1)
	all[0] = uintptr(o.ptr)
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(o.method(index), all...)
	return r
}

func (o comObject) addRef() {
	if o.valid() {
		o.call(1)
	}
}
func (o comObject) release() {
	if o.valid() {
		o.call(2)
	}
}

func lockCameraFrameRate(mediaSource comObject, device Device, fps int) (frameRateControlResult, func() error) {
	var pointer unsafe.Pointer
	hr := mediaSource.call(0, uintptr(unsafe.Pointer(&iidIAMCameraControl)), uintptr(unsafe.Pointer(&pointer)))
	if failed(hr) || pointer == nil {
		return frameRateControlResult{Method: "IAMCameraControl", Err: hrError("IMFMediaSource.QueryInterface(IAMCameraControl)", hr)}, nil
	}
	control := comObject{pointer}
	result := frameRateControlResult{Method: "auto-exposure-priority"}
	hr = control.call(5, cameraControlAutoExposurePriority, uintptr(unsafe.Pointer(&result.BeforeValue)), uintptr(unsafe.Pointer(&result.BeforeFlags)))
	priorityChanged := false
	var earlyRestoreErr error
	if !failed(hr) {
		hr = control.call(4, cameraControlAutoExposurePriority, 0, cameraControlFlagManual)
		if !failed(hr) {
			priorityChanged = true
			hr = control.call(5, cameraControlAutoExposurePriority, uintptr(unsafe.Pointer(&result.AfterValue)), uintptr(unsafe.Pointer(&result.AfterFlags)))
			if !failed(hr) && result.AfterValue == 0 {
				var exposureBeforeValue, exposureBeforeFlags int32
				exposureChanged := false
				hr = control.call(5, cameraControlExposure, uintptr(unsafe.Pointer(&exposureBeforeValue)), uintptr(unsafe.Pointer(&exposureBeforeFlags)))
				if !failed(hr) {
					hr = control.call(4, cameraControlExposure, uintptr(uint32(exposureBeforeValue)), cameraControlFlagAuto)
					exposureChanged = !failed(hr)
				}
				var exposureAfterValue, exposureAfterFlags int32
				if !failed(hr) {
					hr = control.call(5, cameraControlExposure, uintptr(unsafe.Pointer(&exposureAfterValue)), uintptr(unsafe.Pointer(&exposureAfterFlags)))
				}
				if !failed(hr) && exposureAfterFlags&cameraControlFlagAuto != 0 {
					var restoreGain func() error
					if needsCadenceGainSupport(device) {
						result.Gain, restoreGain = supportCadenceWithGain(mediaSource)
					}
					result.Applied = true
					return result, func() error {
						defer control.release()
						var restoreErr error
						if restoreGain != nil {
							restoreErr = restoreGain()
						}
						if err := restoreCameraControl(control, cameraControlExposure, exposureBeforeValue, exposureBeforeFlags); err != nil {
							if restoreErr != nil {
								restoreErr = fmt.Errorf("%v; additionally %w", restoreErr, err)
							} else {
								restoreErr = err
							}
						}
						if err := restoreAutoExposurePriority(control, result.BeforeValue, result.BeforeFlags); err != nil {
							if restoreErr != nil {
								return fmt.Errorf("%v; additionally %w", restoreErr, err)
							}
							return err
						}
						return restoreErr
					}
				}
				if exposureChanged {
					earlyRestoreErr = restoreCameraControl(control, cameraControlExposure, exposureBeforeValue, exposureBeforeFlags)
				}
			}
		}
	}
	if priorityChanged {
		if err := restoreAutoExposurePriority(control, result.BeforeValue, result.BeforeFlags); err != nil {
			control.release()
			result.Err = fmt.Errorf("restore auto exposure priority after failed verification: %w", err)
			if earlyRestoreErr != nil {
				result.Err = fmt.Errorf("%v; additionally %w", earlyRestoreErr, result.Err)
			}
			return result, nil
		}
	}
	if earlyRestoreErr != nil {
		control.release()
		result.Err = fmt.Errorf("restore exposure after failed automatic verification: %w", earlyRestoreErr)
		return result, nil
	}
	if !allowsManualExposureFallback(device) {
		// A generic manual-exposure fallback can permanently darken or otherwise
		// disturb cameras whose driver semantics have not been measured. Leave
		// unknown devices on their current automatic behavior when property 19 is
		// unavailable or ineffective.
		control.release()
		return frameRateControlResult{Method: "automatic-exposure-unmodified"}, nil
	}

	// Drivers without a working AUTO_EXPOSURE_PRIORITY can lengthen exposure
	// beyond the negotiated frame interval. Bound exposure manually for the
	// requested cadence and restore the user's previous setting on close.
	result = frameRateControlResult{Method: "manual-exposure-bound"}
	hr = control.call(3, cameraControlExposure, uintptr(unsafe.Pointer(&result.Minimum)), uintptr(unsafe.Pointer(&result.Maximum)), uintptr(unsafe.Pointer(&result.Step)), uintptr(unsafe.Pointer(&result.Default)), uintptr(unsafe.Pointer(&result.Caps)))
	if failed(hr) {
		control.release()
		result.Err = hrError("IAMCameraControl.GetRange(exposure)", hr)
		return result, nil
	}
	hr = control.call(5, cameraControlExposure, uintptr(unsafe.Pointer(&result.BeforeValue)), uintptr(unsafe.Pointer(&result.BeforeFlags)))
	if failed(hr) {
		control.release()
		result.Err = hrError("IAMCameraControl.Get(exposure)", hr)
		return result, nil
	}
	target := exposureForFPS(result.Minimum, result.Maximum, result.Step, result.BeforeValue, result.BeforeFlags, fps)
	hr = control.call(4, cameraControlExposure, uintptr(uint32(target)), cameraControlFlagManual)
	if failed(hr) {
		control.release()
		result.Err = hrError("IAMCameraControl.Set(exposure frame-rate bound)", hr)
		return result, nil
	}
	hr = control.call(5, cameraControlExposure, uintptr(unsafe.Pointer(&result.AfterValue)), uintptr(unsafe.Pointer(&result.AfterFlags)))
	if failed(hr) {
		restoreHR := restoreCameraControl(control, cameraControlExposure, result.BeforeValue, result.BeforeFlags)
		control.release()
		result.Err = hrError("IAMCameraControl.Get(exposure verify)", hr)
		if restoreHR != nil {
			result.Err = fmt.Errorf("%v; additionally %w", result.Err, restoreHR)
		}
		return result, nil
	}
	result.Applied = result.AfterFlags&cameraControlFlagManual != 0 && result.AfterValue <= target
	if !result.Applied {
		restoreErr := restoreCameraControl(control, cameraControlExposure, result.BeforeValue, result.BeforeFlags)
		control.release()
		result.Err = fmt.Errorf("IAMCameraControl exposure remained value=%d flags=%#x", result.AfterValue, result.AfterFlags)
		if restoreErr != nil {
			result.Err = fmt.Errorf("%v; additionally %w", result.Err, restoreErr)
		}
		return result, nil
	}
	var restoreGain func() error
	if needsCadenceGainSupport(device) {
		result.Gain, restoreGain = supportCadenceWithGain(mediaSource)
	}
	return result, func() error {
		var restoreErr error
		if restoreGain != nil {
			restoreErr = restoreGain()
		}
		defer control.release()
		if err := restoreCameraControl(control, cameraControlExposure, result.BeforeValue, result.BeforeFlags); err != nil {
			if restoreErr != nil {
				return fmt.Errorf("%v; additionally %w", restoreErr, err)
			}
			return err
		}
		return restoreErr
	}
}

func needsCadenceGainSupport(device Device) bool {
	name, id := strings.ToLower(device.Name), strings.ToLower(device.ID)
	return strings.Contains(name, "c922") || (strings.Contains(id, "vid_046d") && strings.Contains(id, "pid_085c"))
}

func allowsManualExposureFallback(device Device) bool {
	return needsCadenceGainSupport(device)
}

// supportCadenceWithGain compensates the one-stop light loss caused by
// bounding a variable-rate camera at 30 fps. The C922 exposes gain only as a
// manual VideoProcAmp control, so switching exposure from auto to manual
// otherwise strands it at its near-zero startup value and crushes the image.
// A quarter of the advertised range is the measured point that matches the
// C922's auto-exposure luminance while keeping a true 30 fps cadence. Existing
// higher user gain is retained, and every change is restored on Close.
func supportCadenceWithGain(mediaSource comObject) (procAmpControlResult, func() error) {
	result := procAmpControlResult{Method: "manual-gain-floor"}
	var pointer unsafe.Pointer
	hr := mediaSource.call(0, uintptr(unsafe.Pointer(&iidIAMVideoProcAmp)), uintptr(unsafe.Pointer(&pointer)))
	if failed(hr) || pointer == nil {
		result.Err = hrError("IMFMediaSource.QueryInterface(IAMVideoProcAmp)", hr)
		return result, nil
	}
	control := comObject{pointer}
	hr = control.call(3, videoProcAmpGain, uintptr(unsafe.Pointer(&result.Minimum)), uintptr(unsafe.Pointer(&result.Maximum)), uintptr(unsafe.Pointer(&result.Step)), uintptr(unsafe.Pointer(&result.Default)), uintptr(unsafe.Pointer(&result.Caps)))
	if failed(hr) {
		control.release()
		result.Err = hrError("IAMVideoProcAmp.GetRange(gain)", hr)
		return result, nil
	}
	hr = control.call(5, videoProcAmpGain, uintptr(unsafe.Pointer(&result.BeforeValue)), uintptr(unsafe.Pointer(&result.BeforeFlags)))
	if failed(hr) {
		control.release()
		result.Err = hrError("IAMVideoProcAmp.Get(gain)", hr)
		return result, nil
	}
	target := gainFloor(result.Minimum, result.Maximum, result.Step, result.BeforeValue)
	if target == result.BeforeValue && result.BeforeFlags&cameraControlFlagManual != 0 {
		result.AfterValue, result.AfterFlags = result.BeforeValue, result.BeforeFlags
		control.release()
		return result, nil
	}
	hr = control.call(4, videoProcAmpGain, uintptr(uint32(target)), cameraControlFlagManual)
	if failed(hr) {
		control.release()
		result.Err = hrError("IAMVideoProcAmp.Set(gain floor)", hr)
		return result, nil
	}
	hr = control.call(5, videoProcAmpGain, uintptr(unsafe.Pointer(&result.AfterValue)), uintptr(unsafe.Pointer(&result.AfterFlags)))
	if failed(hr) {
		restoreErr := restoreVideoProcAmp(control, videoProcAmpGain, result.BeforeValue, result.BeforeFlags)
		control.release()
		result.Err = hrError("IAMVideoProcAmp.Get(gain verify)", hr)
		if restoreErr != nil {
			result.Err = fmt.Errorf("%v; additionally %w", result.Err, restoreErr)
		}
		return result, nil
	}
	result.Applied = result.AfterFlags&cameraControlFlagManual != 0 && result.AfterValue >= target
	if !result.Applied {
		restoreErr := restoreVideoProcAmp(control, videoProcAmpGain, result.BeforeValue, result.BeforeFlags)
		control.release()
		result.Err = fmt.Errorf("IAMVideoProcAmp gain remained value=%d flags=%#x", result.AfterValue, result.AfterFlags)
		if restoreErr != nil {
			result.Err = fmt.Errorf("%v; additionally %w", result.Err, restoreErr)
		}
		return result, nil
	}
	return result, func() error {
		defer control.release()
		return restoreVideoProcAmp(control, videoProcAmpGain, result.BeforeValue, result.BeforeFlags)
	}
}

func gainFloor(minimum, maximum, step, current int32) int32 {
	target := minimum + (maximum-minimum+3)/4
	if target < current {
		target = current
	}
	if step > 0 {
		target = minimum + (target-minimum)/step*step
	}
	if target < minimum {
		return minimum
	}
	if target > maximum {
		return maximum
	}
	return target
}

func exposureForFPS(minimum, maximum, step, current, flags int32, fps int) int32 {
	target := int32(math.Floor(-math.Log2(float64(fps))))
	if target < minimum {
		target = minimum
	}
	if target > maximum {
		target = maximum
	}
	if step > 0 {
		target = minimum + (target-minimum)/step*step
	}
	if flags&cameraControlFlagManual != 0 && current <= target {
		target = current
	}
	return target
}

func restoreCameraControl(control comObject, property int, value, flags int32) error {
	if flags&cameraControlFlagAuto == 0 && flags&cameraControlFlagManual == 0 {
		return fmt.Errorf("IAMCameraControl cannot restore property %d with unsupported saved flags %#x", property, flags)
	}
	hr := control.call(4, uintptr(property), uintptr(uint32(value)), uintptr(uint32(flags)))
	if failed(hr) {
		return hrError("IAMCameraControl restore property", hr)
	}
	var afterValue, afterFlags int32
	hr = control.call(5, uintptr(property), uintptr(unsafe.Pointer(&afterValue)), uintptr(unsafe.Pointer(&afterFlags)))
	if failed(hr) {
		return hrError("IAMCameraControl verify restored property", hr)
	}
	if flags&cameraControlFlagAuto != 0 {
		if afterFlags&cameraControlFlagAuto == 0 {
			return fmt.Errorf("IAMCameraControl property %d restore mode=%#x, want automatic", property, afterFlags)
		}
	} else if afterFlags&cameraControlFlagManual == 0 || afterValue != value {
		return fmt.Errorf("IAMCameraControl property %d restored value=%d flags=%#x, want value=%d manual", property, afterValue, afterFlags, value)
	}
	return nil
}

func restoreAutoExposurePriority(control comObject, value, flags int32) error {
	hr := control.call(4, cameraControlAutoExposurePriority, uintptr(uint32(value)), uintptr(uint32(flags)))
	if failed(hr) {
		return hrError("IAMCameraControl restore auto exposure priority", hr)
	}
	var afterValue, afterFlags int32
	hr = control.call(5, cameraControlAutoExposurePriority, uintptr(unsafe.Pointer(&afterValue)), uintptr(unsafe.Pointer(&afterFlags)))
	if failed(hr) {
		return hrError("IAMCameraControl verify restored auto exposure priority", hr)
	}
	if afterValue != value || afterFlags != flags {
		return fmt.Errorf("IAMCameraControl auto exposure priority restored value=%d flags=%#x, want value=%d flags=%#x", afterValue, afterFlags, value, flags)
	}
	return nil
}

func restoreVideoProcAmp(control comObject, property int, value, flags int32) error {
	if flags&cameraControlFlagAuto == 0 && flags&cameraControlFlagManual == 0 {
		return fmt.Errorf("IAMVideoProcAmp cannot restore property %d with unsupported saved flags %#x", property, flags)
	}
	hr := control.call(4, uintptr(property), uintptr(uint32(value)), uintptr(uint32(flags)))
	if failed(hr) {
		return hrError("IAMVideoProcAmp restore property", hr)
	}
	var afterValue, afterFlags int32
	hr = control.call(5, uintptr(property), uintptr(unsafe.Pointer(&afterValue)), uintptr(unsafe.Pointer(&afterFlags)))
	if failed(hr) {
		return hrError("IAMVideoProcAmp verify restored property", hr)
	}
	if flags&cameraControlFlagAuto != 0 {
		if afterFlags&cameraControlFlagAuto == 0 {
			return fmt.Errorf("IAMVideoProcAmp property %d restore mode=%#x, want automatic", property, afterFlags)
		}
	} else if afterFlags&cameraControlFlagManual == 0 || afterValue != value {
		return fmt.Errorf("IAMVideoProcAmp property %d restored value=%d flags=%#x, want value=%d manual", property, afterValue, afterFlags, value)
	}
	return nil
}

func failed(hr uintptr) bool { return uint32(hr)&0x80000000 != 0 }
func hrError(operation string, hr uintptr) error {
	return fmt.Errorf("%s failed with HRESULT %#08x", operation, uint32(hr))
}

func requireProcs(procs ...*windows.LazyProc) error {
	for _, proc := range procs {
		if err := proc.Find(); err != nil {
			return fmt.Errorf("Windows API %s is unavailable: %w", proc.Name, err)
		}
	}
	return nil
}

func startMediaFoundation() (func(), error) {
	if err := requireProcs(procMFStartup, procMFShutdown, procMFCreateAttributes, procMFEnumDeviceSources, procMFCreateSourceReaderFromMediaSource, procCoInitializeEx, procCoUninitialize); err != nil {
		return nil, err
	}
	hr, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)
	if failed(hr) {
		return nil, hrError("CoInitializeEx(MTA)", hr)
	}
	hr, _, _ = procMFStartup.Call(mfVersion, mfStartupFull)
	if failed(hr) {
		procCoUninitialize.Call()
		return nil, hrError("MFStartup", hr)
	}
	return func() { procMFShutdown.Call(); procCoUninitialize.Call() }, nil
}

func startWIC() (comObject, func(), error) {
	if err := requireProcs(procCoInitializeEx, procCoUninitialize, procCoCreateInstance); err != nil {
		return comObject{}, nil, err
	}
	hr, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)
	if failed(hr) {
		return comObject{}, nil, hrError("CoInitializeEx(MTA)", hr)
	}
	var factoryPtr unsafe.Pointer
	hr, _, _ = procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidWICImagingFactory)), 0, clsctxInprocServer, uintptr(unsafe.Pointer(&iidIWICImagingFactory)), uintptr(unsafe.Pointer(&factoryPtr)))
	if failed(hr) || factoryPtr == nil {
		procCoUninitialize.Call()
		return comObject{}, nil, hrError("CoCreateInstance(CLSID_WICImagingFactory)", hr)
	}
	factory := comObject{factoryPtr}
	return factory, func() { factory.release(); procCoUninitialize.Call() }, nil
}

type mfActivation struct {
	object comObject
	device Device
}

type activationSet struct {
	array unsafe.Pointer
	items []mfActivation
}

func enumerateActivations() (*activationSet, error) {
	var attrsPtr unsafe.Pointer
	hr, _, _ := procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&attrsPtr)), 1)
	if failed(hr) || attrsPtr == nil {
		return nil, hrError("MFCreateAttributes(device enumeration)", hr)
	}
	attrs := comObject{attrsPtr}
	defer attrs.release()
	if hr = attrs.call(24, uintptr(unsafe.Pointer(&mfDevSourceType)), uintptr(unsafe.Pointer(&mfDevSourceVideo))); failed(hr) {
		return nil, hrError("IMFAttributes.SetGUID(video capture)", hr)
	}
	var array unsafe.Pointer
	var count uint32
	hr, _, _ = procMFEnumDeviceSources.Call(uintptr(attrs.ptr), uintptr(unsafe.Pointer(&array)), uintptr(unsafe.Pointer(&count)))
	if failed(hr) {
		return nil, hrError("MFEnumDeviceSources", hr)
	}
	set := &activationSet{array: array}
	if count > maxDeviceCount {
		set.close()
		return nil, fmt.Errorf("Media Foundation returned %d devices, bound is %d", count, maxDeviceCount)
	}
	if count == 0 {
		set.close()
		return nil, errors.New("Media Foundation found no video capture devices")
	}
	if array == nil {
		set.close()
		return nil, errors.New("Media Foundation returned a null device array")
	}
	pointers := unsafe.Slice((*unsafe.Pointer)(array), int(count))
	set.items = make([]mfActivation, 0, count)
	for _, pointer := range pointers {
		set.items = append(set.items, mfActivation{object: comObject{pointer}})
	}
	for index := range set.items {
		name, nameErr := mfAllocatedString(set.items[index].object, mfFriendlyName)
		id, idErr := mfAllocatedString(set.items[index].object, mfVideoSymbolicLink)
		if nameErr != nil || idErr != nil {
			set.close()
			return nil, fmt.Errorf("read Media Foundation device identity: name=%v id=%v", nameErr, idErr)
		}
		set.items[index].device = Device{Name: name, ID: id}
	}
	return set, nil
}

func (s *activationSet) close() {
	if s == nil {
		return
	}
	for index := len(s.items) - 1; index >= 0; index-- {
		s.items[index].object.release()
	}
	s.items = nil
	if s.array != nil {
		windows.CoTaskMemFree(s.array)
		s.array = nil
	}
}

func (s *activationSet) exact(id string) (mfActivation, error) {
	for _, activation := range s.items {
		if strings.EqualFold(activation.device.ID, id) {
			return activation, nil
		}
	}
	return mfActivation{}, fmt.Errorf("Media Foundation device ID %q was not found", id)
}

func mfAllocatedString(object comObject, key windows.GUID) (string, error) {
	var pointer unsafe.Pointer
	var length uint32
	hr := object.call(13, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&pointer)), uintptr(unsafe.Pointer(&length)))
	if failed(hr) {
		return "", hrError("IMFAttributes.GetAllocatedString", hr)
	}
	if pointer == nil {
		return "", errors.New("IMFAttributes.GetAllocatedString returned null")
	}
	defer windows.CoTaskMemFree(pointer)
	if length > 32768 {
		return "", fmt.Errorf("Media Foundation string length %d exceeds bound", length)
	}
	return windows.UTF16ToString(unsafe.Slice((*uint16)(pointer), int(length)+1)), nil
}

func inspectMediaType(mediaType comObject) (Mode, error) {
	var major, subtype windows.GUID
	if hr := mediaType.call(10, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&major))); failed(hr) {
		return Mode{}, hrError("IMFMediaType.GetGUID(MAJOR_TYPE)", hr)
	}
	if major != mfMediaTypeVideo {
		return Mode{}, errors.New("media type is not video")
	}
	if hr := mediaType.call(10, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&subtype))); failed(hr) {
		return Mode{}, hrError("IMFMediaType.GetGUID(SUBTYPE)", hr)
	}
	var size, rate uint64
	if hr := mediaType.call(8, uintptr(unsafe.Pointer(&mfMTFrameSize)), uintptr(unsafe.Pointer(&size))); failed(hr) {
		return Mode{}, hrError("IMFMediaType.GetUINT64(FRAME_SIZE)", hr)
	}
	if hr := mediaType.call(8, uintptr(unsafe.Pointer(&mfMTFrameRate)), uintptr(unsafe.Pointer(&rate))); failed(hr) {
		return Mode{}, hrError("IMFMediaType.GetUINT64(FRAME_RATE)", hr)
	}
	name := subtype.String()
	if subtype == mfVideoFormatMJPG {
		name = "MJPG"
	}
	return Mode{Width: int(size >> 32), Height: int(uint32(size)), FPSNumerator: uint32(rate >> 32), FPSDenominator: uint32(rate), Format: name}, nil
}

func mediaTypeMatches(left, right Mode) bool {
	return left.Format == "MJPG" && right.Format == "MJPG" && left.Width == right.Width && left.Height == right.Height && left.FPSNumerator == right.FPSNumerator && left.FPSDenominator == right.FPSDenominator
}

func selectBestNativeType(reader comObject, cfg Config) (comObject, Mode, error) {
	target := Mode{Width: cfg.Width, Height: cfg.Height, FPSNumerator: uint32(cfg.FPS), FPSDenominator: 1, Format: "MJPG"}
	candidates := make([]modeCandidate, 0, 16)
	for index := 0; index < maxNativeTypes; index++ {
		var pointer unsafe.Pointer
		hr := reader.call(5, mfSourceReaderFirstVideoStream, uintptr(index), uintptr(unsafe.Pointer(&pointer)))
		if uint32(hr) == mfENoMoreTypes {
			break
		}
		if failed(hr) {
			return comObject{}, Mode{}, hrError(fmt.Sprintf("IMFSourceReader.GetNativeMediaType(%d)", index), hr)
		}
		if pointer == nil {
			return comObject{}, Mode{}, fmt.Errorf("IMFSourceReader.GetNativeMediaType(%d) returned null", index)
		}
		mediaType := comObject{pointer}
		info, err := inspectMediaType(mediaType)
		if err == nil && info.Format == "MJPG" && validCaptureMode(info) {
			candidates = append(candidates, modeCandidate{Mode: info, Index: index})
		}
		mediaType.release()
	}
	selected, ok := chooseBestMode(candidates, target)
	if !ok {
		return comObject{}, Mode{}, fmt.Errorf("camera does not expose a usable native MJPG mode near %dx%d@%d", cfg.Width, cfg.Height, cfg.FPS)
	}
	var pointer unsafe.Pointer
	hr := reader.call(5, mfSourceReaderFirstVideoStream, uintptr(selected.Index), uintptr(unsafe.Pointer(&pointer)))
	if failed(hr) {
		return comObject{}, Mode{}, hrError(fmt.Sprintf("IMFSourceReader.GetNativeMediaType(%d selected)", selected.Index), hr)
	}
	if pointer == nil {
		return comObject{}, Mode{}, fmt.Errorf("IMFSourceReader.GetNativeMediaType(%d selected) returned null", selected.Index)
	}
	mediaType := comObject{pointer}
	verified, err := inspectMediaType(mediaType)
	if err != nil {
		mediaType.release()
		return comObject{}, Mode{}, err
	}
	if !mediaTypeMatches(verified, selected.Mode) {
		mediaType.release()
		return comObject{}, Mode{}, fmt.Errorf("native mode at index %d changed from %+v to %+v during selection", selected.Index, selected.Mode, verified)
	}
	return mediaType, selected.Mode, nil
}

func copyMFSample(sample comObject, readerTimestamp int64, readerFlags uint32, maxPacketBytes int, pool *packetPool) (Packet, error) {
	metadata := Packet{ReaderTimestamp100ns: readerTimestamp, ReaderFlags: readerFlags}
	if hr := sample.call(33, uintptr(unsafe.Pointer(&metadata.SampleFlags))); failed(hr) {
		return Packet{}, hrError("IMFSample.GetSampleFlags", hr)
	}
	if hr := sample.call(35, uintptr(unsafe.Pointer(&metadata.SampleTimestamp100ns))); failed(hr) {
		return Packet{}, hrError("IMFSample.GetSampleTime", hr)
	}
	if hr := sample.call(37, uintptr(unsafe.Pointer(&metadata.SampleDuration100ns))); !failed(hr) {
		metadata.DurationKnown = true
	}
	if hr := sample.call(39, uintptr(unsafe.Pointer(&metadata.BufferCount))); failed(hr) {
		return Packet{}, hrError("IMFSample.GetBufferCount", hr)
	}
	var bufferPtr unsafe.Pointer
	if hr := sample.call(41, uintptr(unsafe.Pointer(&bufferPtr))); failed(hr) || bufferPtr == nil {
		return Packet{}, hrError("IMFSample.ConvertToContiguousBuffer", hr)
	}
	buffer := comObject{bufferPtr}
	defer buffer.release()
	var data unsafe.Pointer
	var maximum, current uint32
	if hr := buffer.call(3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&maximum)), uintptr(unsafe.Pointer(&current))); failed(hr) {
		return Packet{}, hrError("IMFMediaBuffer.Lock", hr)
	}
	locked := true
	defer func() {
		if locked {
			buffer.call(4)
		}
	}()
	if current > maximum {
		return Packet{}, fmt.Errorf("Media Foundation buffer length %d exceeds maximum %d", current, maximum)
	}
	if current == 0 || int64(current) > int64(maxPacketBytes) {
		return Packet{}, fmt.Errorf("Media Foundation MJPEG length %d is outside 1..%d", current, maxPacketBytes)
	}
	if data == nil {
		return Packet{}, errors.New("Media Foundation locked a null buffer")
	}
	packet, err := pool.acquire(int(current))
	if err != nil {
		return Packet{}, err
	}
	packet.ReaderTimestamp100ns = metadata.ReaderTimestamp100ns
	packet.ReaderFlags = metadata.ReaderFlags
	packet.SampleFlags = metadata.SampleFlags
	packet.SampleTimestamp100ns = metadata.SampleTimestamp100ns
	packet.SampleDuration100ns = metadata.SampleDuration100ns
	packet.DurationKnown = metadata.DurationKnown
	packet.BufferCount = metadata.BufferCount
	copy(packet.Data, unsafe.Slice((*byte)(data), int(current)))
	if hr := buffer.call(4); failed(hr) {
		packet.Release()
		return Packet{}, hrError("IMFMediaBuffer.Unlock", hr)
	}
	locked = false
	if len(packet.Data) < 4 || packet.Data[0] != 0xff || packet.Data[1] != 0xd8 || packet.Data[len(packet.Data)-2] != 0xff || packet.Data[len(packet.Data)-1] != 0xd9 {
		tailStart := len(packet.Data) - 16
		if tailStart < 0 {
			tailStart = 0
		}
		headEnd := 16
		if headEnd > len(packet.Data) {
			headEnd = len(packet.Data)
		}
		err := fmt.Errorf("native IMFSample is not one complete JPEG boundary: bytes=%d first_soi=%d last_eoi=%d head=%x tail=%x", len(packet.Data), bytes.Index(packet.Data, []byte{0xff, 0xd8}), bytes.LastIndex(packet.Data, []byte{0xff, 0xd9}), packet.Data[:headEnd], packet.Data[tailStart:])
		packet.Release()
		return Packet{}, err
	}
	return packet, nil
}

func guid(data1 uint32, data2, data3 uint16, data4 ...byte) windows.GUID {
	var tail [8]byte
	copy(tail[:], data4)
	return windows.GUID{Data1: data1, Data2: data2, Data3: data3, Data4: tail}
}
