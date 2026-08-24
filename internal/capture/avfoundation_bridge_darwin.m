#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <Foundation/Foundation.h>
#import <objc/message.h>
#import <stdatomic.h>
#import <stdlib.h>
#import <string.h>

#include "avfoundation_bridge_darwin.h"
#include "_cgo_export.h"

typedef struct {
    CVPixelBufferRef pixelBuffer;
} InlaidAVFFrame;

static char *InlaidCopyString(NSString *value) {
    if (value == nil) {
        return NULL;
    }
    const char *utf8 = value.UTF8String;
    return utf8 == NULL ? NULL : strdup(utf8);
}

static char *InlaidError(NSString *operation, NSError *error) {
    NSString *detail = error.localizedDescription;
    if (detail.length == 0) {
        detail = @"unknown error";
    }
    return InlaidCopyString([NSString stringWithFormat:@"%@: %@", operation, detail]);
}

static char *InlaidException(NSString *operation, NSException *exception) {
    NSString *detail = exception.reason;
    if (detail.length == 0) {
        detail = exception.name;
    }
    return InlaidCopyString([NSString stringWithFormat:@"%@: %@", operation, detail]);
}

static NSArray<AVCaptureDevice *> *InlaidVideoDevices(void) {
    AVCaptureDeviceDiscoverySession *discovery = [AVCaptureDeviceDiscoverySession
        discoverySessionWithDeviceTypes:@[
            AVCaptureDeviceTypeBuiltInWideAngleCamera,
            AVCaptureDeviceTypeExternalUnknown,
        ]
        mediaType:AVMediaTypeVideo
        position:AVCaptureDevicePositionUnspecified];
    return discovery.devices;
}

static AVCaptureDevice *InlaidExactDevice(NSString *uniqueID) {
    if (uniqueID.length == 0) {
        return nil;
    }
    for (AVCaptureDevice *device in InlaidVideoDevices()) {
        if ([device.uniqueID isEqualToString:uniqueID]) {
            return device;
        }
    }
    return nil;
}

static NSString *InlaidFourCC(FourCharCode value) {
    uint32_t bigEndian = CFSwapInt32HostToBig(value);
    unsigned char bytes[4];
    memcpy(bytes, &bigEndian, sizeof(bytes));
    BOOL printable = YES;
    for (NSUInteger index = 0; index < sizeof(bytes); index++) {
        if (bytes[index] < 0x20 || bytes[index] > 0x7e) {
            printable = NO;
            break;
        }
    }
    if (printable) {
        return [[NSString alloc] initWithBytes:bytes length:sizeof(bytes) encoding:NSASCIIStringEncoding];
    }
    return [NSString stringWithFormat:@"0x%08x", (unsigned int)value];
}

static BOOL InlaidNumericTime(CMTime value) {
    return CMTIME_IS_NUMERIC(value) && value.value > 0 && value.timescale > 0;
}

static BOOL InlaidRangeContainsDuration(AVFrameRateRange *range, CMTime duration) {
    return InlaidNumericTime(range.minFrameDuration) &&
        InlaidNumericTime(range.maxFrameDuration) &&
        CMTimeCompare(duration, range.minFrameDuration) >= 0 &&
        CMTimeCompare(duration, range.maxFrameDuration) <= 0;
}

static OSType InlaidNV12Format(AVCaptureVideoDataOutput *output) {
    for (NSNumber *format in output.availableVideoCVPixelFormatTypes) {
        OSType value = format.unsignedIntValue;
        if (value == kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange ||
            value == kCVPixelFormatType_420YpCbCr8BiPlanarFullRange) {
            return value;
        }
    }
    return 0;
}

static void InlaidSetBooleanSelector(id object, NSString *selectorName, BOOL value) {
    SEL selector = NSSelectorFromString(selectorName);
    if ([object respondsToSelector:selector]) {
        ((void (*)(id, SEL, BOOL))objc_msgSend)(object, selector, value);
    }
}

static int InlaidMatrix(CVPixelBufferRef pixelBuffer, CMFormatDescriptionRef description, int configuredMatrix) {
    CFTypeRef value = CVBufferGetAttachment(pixelBuffer, kCVImageBufferYCbCrMatrixKey, NULL);
    if (value == NULL && description != NULL) {
        CFDictionaryRef extensions = CMFormatDescriptionGetExtensions(description);
        if (extensions != NULL) {
            value = CFDictionaryGetValue(extensions, kCMFormatDescriptionExtension_YCbCrMatrix);
        }
    }
    if (value != NULL && CFEqual(value, kCVImageBufferYCbCrMatrix_ITU_R_601_4)) {
        return 601;
    }
    if (value != NULL && CFEqual(value, kCVImageBufferYCbCrMatrix_ITU_R_709_2)) {
        return 709;
    }
    return configuredMatrix;
}

static NSDictionary<NSString *, NSString *> *InlaidColorProperties(int matrix) {
    NSString *primaries = matrix == 709
        ? AVVideoColorPrimaries_ITU_R_709_2
        : AVVideoColorPrimaries_SMPTE_C;
    NSString *matrixName = matrix == 709
        ? AVVideoYCbCrMatrix_ITU_R_709_2
        : AVVideoYCbCrMatrix_ITU_R_601_4;
    return @{
        AVVideoColorPrimariesKey: primaries,
        AVVideoTransferFunctionKey: AVVideoTransferFunction_ITU_R_709_2,
        AVVideoYCbCrMatrixKey: matrixName,
    };
}

@interface InlaidAVFCapture : NSObject <AVCaptureVideoDataOutputSampleBufferDelegate> {
    atomic_bool _acceptingCallbacks;
}

@property(nonatomic, strong) AVCaptureSession *session;
@property(nonatomic, strong) AVCaptureDevice *device;
@property(nonatomic, strong) AVCaptureDeviceInput *input;
@property(nonatomic, strong) AVCaptureVideoDataOutput *output;
@property(nonatomic, strong) NSMutableArray *observerTokens;
@property(nonatomic, strong) dispatch_queue_t sessionQueue;
@property(nonatomic, strong) dispatch_queue_t sampleQueue;
@property(nonatomic, strong) dispatch_group_t callbackGroup;
@property(nonatomic) uintptr_t goHandle;
@property(nonatomic) int configuredMatrix;
@property(nonatomic) BOOL started;

- (BOOL)beginGoCallback;
- (void)endGoCallback;
- (void)acceptCallbacks;
- (void)rejectCallbacks;
- (void)sendError:(NSString *)message temporary:(BOOL)temporary;
- (void)installObservers;
- (void)removeObservers;

@end

@implementation InlaidAVFCapture

- (instancetype)init {
    self = [super init];
    if (self != nil) {
        atomic_init(&_acceptingCallbacks, false);
        _observerTokens = [NSMutableArray array];
        _sessionQueue = dispatch_queue_create("land.melty.inlaid.avfoundation.session", DISPATCH_QUEUE_SERIAL);
        _sampleQueue = dispatch_queue_create("land.melty.inlaid.avfoundation.samples", DISPATCH_QUEUE_SERIAL);
        _callbackGroup = dispatch_group_create();
    }
    return self;
}

- (BOOL)beginGoCallback {
    if (!atomic_load_explicit(&_acceptingCallbacks, memory_order_acquire)) {
        return NO;
    }
    dispatch_group_enter(self.callbackGroup);
    if (!atomic_load_explicit(&_acceptingCallbacks, memory_order_acquire)) {
        dispatch_group_leave(self.callbackGroup);
        return NO;
    }
    return YES;
}

- (void)endGoCallback {
    dispatch_group_leave(self.callbackGroup);
}

- (void)acceptCallbacks {
    atomic_store_explicit(&_acceptingCallbacks, true, memory_order_release);
}

- (void)rejectCallbacks {
    atomic_store_explicit(&_acceptingCallbacks, false, memory_order_release);
}

- (void)sendError:(NSString *)message temporary:(BOOL)temporary {
    if (message.length == 0 || ![self beginGoCallback]) {
        return;
    }
    inlaidAVFError(self.goHandle, (char *)message.UTF8String, temporary ? 1 : 0);
    [self endGoCallback];
}

- (void)installObservers {
    NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
    __weak InlaidAVFCapture *weakSelf = self;
    id runtime = [center addObserverForName:AVCaptureSessionRuntimeErrorNotification
                                     object:self.session
                                      queue:nil
                                 usingBlock:^(NSNotification *notification) {
        InlaidAVFCapture *strongSelf = weakSelf;
        NSError *error = notification.userInfo[AVCaptureSessionErrorKey];
        NSString *message = error == nil
            ? @"AVFoundation camera session stopped with an unknown runtime error"
            : [NSString stringWithFormat:@"AVFoundation camera session: %@", error.localizedDescription];
        [strongSelf sendError:message temporary:NO];
    }];
    id interrupted = [center addObserverForName:AVCaptureSessionWasInterruptedNotification
                                         object:self.session
                                          queue:nil
                                     usingBlock:^(NSNotification *notification) {
        InlaidAVFCapture *strongSelf = weakSelf;
        NSNumber *reason = notification.userInfo[AVCaptureSessionInterruptionReasonKey];
        NSString *message = reason == nil
            ? @"AVFoundation camera session was interrupted"
            : [NSString stringWithFormat:@"AVFoundation camera session was interrupted (reason %@)", reason];
        [strongSelf sendError:message temporary:YES];
    }];
    id disconnected = [center addObserverForName:AVCaptureDeviceWasDisconnectedNotification
                                           object:self.device
                                            queue:nil
                                       usingBlock:^(NSNotification *notification) {
        InlaidAVFCapture *strongSelf = weakSelf;
        [strongSelf sendError:@"The selected camera was disconnected" temporary:NO];
    }];
    [self.observerTokens addObjectsFromArray:@[runtime, interrupted, disconnected]];
}

- (void)removeObservers {
    NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
    for (id token in self.observerTokens) {
        [center removeObserver:token];
    }
    [self.observerTokens removeAllObjects];
}

- (void)captureOutput:(AVCaptureOutput *)output
    didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
       fromConnection:(AVCaptureConnection *)connection {
    if (![self beginGoCallback]) {
        return;
    }
    CVPixelBufferRef pixelBuffer = CMSampleBufferGetImageBuffer(sampleBuffer);
    if (pixelBuffer == NULL) {
        inlaidAVFError(self.goHandle, "AVFoundation delivered a sample without a pixel buffer", 1);
        [self endGoCallback];
        return;
    }
    OSType format = CVPixelBufferGetPixelFormatType(pixelBuffer);
    if ((format != kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange &&
         format != kCVPixelFormatType_420YpCbCr8BiPlanarFullRange) ||
        !CVPixelBufferIsPlanar(pixelBuffer) || CVPixelBufferGetPlaneCount(pixelBuffer) < 2) {
        inlaidAVFError(self.goHandle, "AVFoundation delivered a non-NV12 pixel buffer", 0);
        [self endGoCallback];
        return;
    }
    CFRetain(pixelBuffer);
    CVReturn lockResult = CVPixelBufferLockBaseAddress(pixelBuffer, kCVPixelBufferLock_ReadOnly);
    if (lockResult != kCVReturnSuccess) {
        CFRelease(pixelBuffer);
        inlaidAVFError(self.goHandle, "AVFoundation could not lock an NV12 pixel buffer", 1);
        [self endGoCallback];
        return;
    }
    InlaidAVFFrame *frame = calloc(1, sizeof(InlaidAVFFrame));
    if (frame == NULL) {
        CVPixelBufferUnlockBaseAddress(pixelBuffer, kCVPixelBufferLock_ReadOnly);
        CFRelease(pixelBuffer);
        inlaidAVFError(self.goHandle, "AVFoundation could not allocate frame ownership", 1);
        [self endGoCallback];
        return;
    }
    frame->pixelBuffer = pixelBuffer;
    size_t width = CVPixelBufferGetWidthOfPlane(pixelBuffer, 0);
    size_t height = CVPixelBufferGetHeightOfPlane(pixelBuffer, 0);
    size_t yStride = CVPixelBufferGetBytesPerRowOfPlane(pixelBuffer, 0);
    size_t uvStride = CVPixelBufferGetBytesPerRowOfPlane(pixelBuffer, 1);
    size_t uvHeight = CVPixelBufferGetHeightOfPlane(pixelBuffer, 1);
    if (height == 0 || uvHeight == 0 || yStride > SIZE_MAX / height || uvStride > SIZE_MAX / uvHeight) {
        inlaid_avf_frame_release(frame);
        inlaidAVFError(self.goHandle, "AVFoundation delivered invalid NV12 plane dimensions", 0);
        [self endGoCallback];
        return;
    }
    CMTime presentationTime = CMSampleBufferGetPresentationTimeStamp(sampleBuffer);
    int64_t ptsValue = 0;
    int32_t ptsTimescale = 0;
    if (CMTIME_IS_NUMERIC(presentationTime)) {
        ptsValue = presentationTime.value;
        ptsTimescale = presentationTime.timescale;
    }
    int matrix = InlaidMatrix(pixelBuffer, CMSampleBufferGetFormatDescription(sampleBuffer), self.configuredMatrix);
    int accepted = inlaidAVFDeliver(
        self.goHandle,
        frame,
        CVPixelBufferGetBaseAddressOfPlane(pixelBuffer, 0),
        yStride * height,
        CVPixelBufferGetBaseAddressOfPlane(pixelBuffer, 1),
        uvStride * uvHeight,
        (int)width,
        (int)height,
        yStride,
        uvStride,
        (uint32_t)format,
        matrix,
        ptsValue,
        ptsTimescale);
    if (!accepted) {
        inlaid_avf_frame_release(frame);
    }
    [self endGoCallback];
}

- (void)captureOutput:(AVCaptureOutput *)output
    didDropSampleBuffer:(CMSampleBufferRef)sampleBuffer
       fromConnection:(AVCaptureConnection *)connection {
    if (![self beginGoCallback]) {
        return;
    }
    inlaidAVFDropped(self.goHandle);
    [self endGoCallback];
}

@end

char *inlaid_avf_authorize(void) {
    @autoreleasepool {
        AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeVideo];
        if (status == AVAuthorizationStatusAuthorized) {
            return NULL;
        }
        if (status == AVAuthorizationStatusDenied) {
            return InlaidCopyString(@"Camera access is denied. Allow Inlaid or its terminal host in System Settings > Privacy & Security > Camera.");
        }
        if (status == AVAuthorizationStatusRestricted) {
            return InlaidCopyString(@"Camera access is restricted by macOS policy.");
        }
        dispatch_semaphore_t permission = dispatch_semaphore_create(0);
        __block BOOL granted = NO;
        [AVCaptureDevice requestAccessForMediaType:AVMediaTypeVideo completionHandler:^(BOOL allowed) {
            granted = allowed;
            dispatch_semaphore_signal(permission);
        }];
        dispatch_semaphore_wait(permission, DISPATCH_TIME_FOREVER);
        if (!granted) {
            return InlaidCopyString(@"Camera access was not granted. A packaged build must include NSCameraUsageDescription; allow Inlaid or its terminal host in System Settings > Privacy & Security > Camera.");
        }
        return NULL;
    }
}

char *inlaid_avf_devices_json(char **error_message) {
    @autoreleasepool {
        if (error_message != NULL) {
            *error_message = NULL;
        }
        @try {
            NSMutableArray *items = [NSMutableArray array];
            for (AVCaptureDevice *device in InlaidVideoDevices()) {
                [items addObject:@{
                    @"name": device.localizedName ?: @"Camera",
                    @"id": device.uniqueID ?: @"",
                }];
            }
            NSError *error = nil;
            NSData *data = [NSJSONSerialization dataWithJSONObject:items options:0 error:&error];
            if (data == nil) {
                if (error_message != NULL) {
                    *error_message = InlaidError(@"encode AVFoundation camera inventory", error);
                }
                return NULL;
            }
            NSString *json = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
            return InlaidCopyString(json);
        } @catch (NSException *exception) {
            if (error_message != NULL) {
                *error_message = InlaidException(@"enumerate AVFoundation cameras", exception);
            }
            return NULL;
        }
    }
}

char *inlaid_avf_modes_json(const char *device_id, char **error_message) {
    @autoreleasepool {
        if (error_message != NULL) {
            *error_message = NULL;
        }
        @try {
            NSString *uniqueID = device_id == NULL ? @"" : [NSString stringWithUTF8String:device_id];
            AVCaptureDevice *device = InlaidExactDevice(uniqueID);
            if (device == nil) {
                if (error_message != NULL) {
                    *error_message = InlaidCopyString([NSString stringWithFormat:@"camera with exact AVFoundation uniqueID %@ is unavailable", uniqueID]);
                }
                return NULL;
            }
            NSMutableArray *items = [NSMutableArray array];
            [device.formats enumerateObjectsUsingBlock:^(AVCaptureDeviceFormat *format, NSUInteger formatIndex, BOOL *stop) {
                CMFormatDescriptionRef description = format.formatDescription;
                CMVideoDimensions dimensions = CMVideoFormatDescriptionGetDimensions(description);
                NSString *subtype = InlaidFourCC(CMFormatDescriptionGetMediaSubType(description));
                for (AVFrameRateRange *range in format.videoSupportedFrameRateRanges) {
                    if (!InlaidNumericTime(range.minFrameDuration) || !InlaidNumericTime(range.maxFrameDuration)) {
                        continue;
                    }
                    [items addObject:@{
                        @"format_index": @(formatIndex),
                        @"width": @(dimensions.width),
                        @"height": @(dimensions.height),
                        @"format": subtype,
                        @"subtype": @(CMFormatDescriptionGetMediaSubType(description)),
                        @"minimum_value": @(range.minFrameDuration.value),
                        @"minimum_timescale": @(range.minFrameDuration.timescale),
                        @"maximum_value": @(range.maxFrameDuration.value),
                        @"maximum_timescale": @(range.maxFrameDuration.timescale),
                    }];
                }
            }];
            NSError *error = nil;
            NSData *data = [NSJSONSerialization dataWithJSONObject:items options:0 error:&error];
            if (data == nil) {
                if (error_message != NULL) {
                    *error_message = InlaidError(@"encode AVFoundation camera modes", error);
                }
                return NULL;
            }
            NSString *json = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
            return InlaidCopyString(json);
        } @catch (NSException *exception) {
            if (error_message != NULL) {
                *error_message = InlaidException(@"enumerate AVFoundation camera modes", exception);
            }
            return NULL;
        }
    }
}

void *inlaid_avf_create(
    const char *device_id,
    int format_index,
    uint32_t source_subtype,
    int source_width,
    int source_height,
    int64_t frame_duration_value,
    int32_t frame_duration_timescale,
    int output_width,
    int output_height,
    int allow_variable_frame_rate,
    uintptr_t go_handle,
    char **error_message) {
    @autoreleasepool {
        if (error_message != NULL) {
            *error_message = NULL;
        }
        @try {
            NSString *uniqueID = device_id == NULL ? @"" : [NSString stringWithUTF8String:device_id];
            AVCaptureDevice *device = InlaidExactDevice(uniqueID);
            if (device == nil) {
                if (error_message != NULL) {
                    *error_message = InlaidCopyString([NSString stringWithFormat:@"camera with exact AVFoundation uniqueID %@ is unavailable", uniqueID]);
                }
                return NULL;
            }
            if (format_index < 0 || format_index >= (int)device.formats.count) {
                if (error_message != NULL) {
                    *error_message = InlaidCopyString(@"selected AVFoundation camera format is no longer available");
                }
                return NULL;
            }
            CMTime frameDuration = CMTimeMake(frame_duration_value, frame_duration_timescale);
            AVCaptureDeviceFormat *format = device.formats[(NSUInteger)format_index];
            CMVideoDimensions nativeDimensions = CMVideoFormatDescriptionGetDimensions(format.formatDescription);
            FourCharCode nativeSubtype = CMFormatDescriptionGetMediaSubType(format.formatDescription);
            if (nativeDimensions.width != source_width || nativeDimensions.height != source_height || nativeSubtype != source_subtype) {
                if (error_message != NULL) {
                    *error_message = InlaidCopyString(@"selected AVFoundation camera format changed during session setup");
                }
                return NULL;
            }
            AVFrameRateRange *selectedRange = nil;
            for (AVFrameRateRange *range in format.videoSupportedFrameRateRanges) {
                if (InlaidRangeContainsDuration(range, frameDuration)) {
                    selectedRange = range;
                    break;
                }
            }
            if (selectedRange == nil) {
                if (error_message != NULL) {
                    *error_message = InlaidCopyString(@"selected AVFoundation frame duration is not supported by the camera format");
                }
                return NULL;
            }
            NSError *inputError = nil;
            AVCaptureDeviceInput *input = [AVCaptureDeviceInput deviceInputWithDevice:device error:&inputError];
            if (input == nil) {
                if (error_message != NULL) {
                    *error_message = InlaidError(@"open AVFoundation camera input", inputError);
                }
                return NULL;
            }
            InlaidAVFCapture *capture = [[InlaidAVFCapture alloc] init];
            capture.goHandle = go_handle;
            capture.device = device;
            capture.input = input;
            capture.session = [[AVCaptureSession alloc] init];
            capture.output = [[AVCaptureVideoDataOutput alloc] init];
            capture.output.alwaysDiscardsLateVideoFrames = YES;
            InlaidSetBooleanSelector(capture.output, @"setAutomaticallyConfiguresOutputBufferDimensions:", NO);
            InlaidSetBooleanSelector(capture.output, @"setDeliversPreviewSizedOutputBuffers:", NO);

            __block char *configurationError = NULL;
            dispatch_sync(capture.sessionQueue, ^{
                [capture.session beginConfiguration];
                @try {
                    capture.session.sessionPreset = AVCaptureSessionPresetInputPriority;
                    if (![capture.session canAddInput:input]) {
                        configurationError = InlaidCopyString(@"AVFoundation cannot add the selected camera input");
                        return;
                    }
                    [capture.session addInput:input];

                    NSError *lockError = nil;
                    if (![device lockForConfiguration:&lockError]) {
                        configurationError = InlaidError(@"configure AVFoundation camera", lockError);
                        return;
                    }
                    @try {
                        device.activeFormat = format;
                        device.activeVideoMinFrameDuration = frameDuration;
                        device.activeVideoMaxFrameDuration = allow_variable_frame_rate
                            ? selectedRange.maxFrameDuration
                            : frameDuration;
                        CMTime expectedMaximum = allow_variable_frame_rate ? selectedRange.maxFrameDuration : frameDuration;
                        if (device.activeFormat != format ||
                            CMTimeCompare(device.activeVideoMinFrameDuration, frameDuration) != 0 ||
                            CMTimeCompare(device.activeVideoMaxFrameDuration, expectedMaximum) != 0) {
                            configurationError = InlaidCopyString(@"AVFoundation did not accept the selected camera format and frame cadence");
                        }
                    } @finally {
                        [device unlockForConfiguration];
                    }
                    if (configurationError != NULL) {
                        return;
                    }

                    if (![capture.session canAddOutput:capture.output]) {
                        configurationError = InlaidCopyString(@"AVFoundation cannot add an NV12 video output");
                        return;
                    }
                    [capture.session addOutput:capture.output];
                    OSType pixelFormat = InlaidNV12Format(capture.output);
                    if (pixelFormat == 0) {
                        configurationError = InlaidCopyString(@"AVFoundation camera exposes neither 420v nor 420f NV12 output");
                        return;
                    }
                    CMVideoDimensions sourceDimensions = CMVideoFormatDescriptionGetDimensions(format.formatDescription);
                    capture.configuredMatrix = sourceDimensions.height > 576 ? 709 : 601;
                    capture.output.videoSettings = @{
                        (NSString *)kCVPixelBufferPixelFormatTypeKey: @(pixelFormat),
                        (NSString *)kCVPixelBufferWidthKey: @(output_width),
                        (NSString *)kCVPixelBufferHeightKey: @(output_height),
                        AVVideoColorPropertiesKey: InlaidColorProperties(capture.configuredMatrix),
                    };
                    [capture.output setSampleBufferDelegate:capture queue:capture.sampleQueue];
                } @catch (NSException *exception) {
                    configurationError = InlaidException(@"configure AVFoundation capture session", exception);
                } @finally {
                    [capture.session commitConfiguration];
                }
            });
            if (configurationError != NULL) {
                [capture.output setSampleBufferDelegate:nil queue:NULL];
                if (error_message != NULL) {
                    *error_message = configurationError;
                } else {
                    free(configurationError);
                }
                return NULL;
            }
            return (__bridge_retained void *)capture;
        } @catch (NSException *exception) {
            if (error_message != NULL) {
                *error_message = InlaidException(@"create AVFoundation capture session", exception);
            }
            return NULL;
        }
    }
}

char *inlaid_avf_start(void *capture_pointer) {
    @autoreleasepool {
        if (capture_pointer == NULL) {
            return InlaidCopyString(@"AVFoundation capture session is unavailable");
        }
        InlaidAVFCapture *capture = (__bridge InlaidAVFCapture *)capture_pointer;
        __block char *startError = NULL;
        dispatch_sync(capture.sessionQueue, ^{
            @try {
                [capture installObservers];
                [capture acceptCallbacks];
                [capture.session startRunning];
                capture.started = capture.session.isRunning;
                if (!capture.started) {
                    [capture rejectCallbacks];
                    [capture removeObservers];
                    startError = InlaidCopyString(@"AVFoundation capture session did not start");
                }
            } @catch (NSException *exception) {
                [capture rejectCallbacks];
                [capture removeObservers];
                startError = InlaidException(@"start AVFoundation capture session", exception);
            }
        });
        return startError;
    }
}

int inlaid_avf_close(void *capture_pointer, int64_t timeout_milliseconds, char **error_message) {
    @autoreleasepool {
        if (error_message != NULL) {
            *error_message = NULL;
        }
        if (capture_pointer == NULL) {
            return 0;
        }
        InlaidAVFCapture *capture = (__bridge InlaidAVFCapture *)capture_pointer;
        [capture rejectCallbacks];
        int64_t boundedMilliseconds = timeout_milliseconds < 1 ? 1 : timeout_milliseconds;
        dispatch_time_t deadline = dispatch_time(DISPATCH_TIME_NOW, boundedMilliseconds * NSEC_PER_MSEC);
        dispatch_semaphore_t finished = dispatch_semaphore_create(0);
        __block BOOL callbacksDrained = NO;
        dispatch_async(capture.sessionQueue, ^{
            [capture.output setSampleBufferDelegate:nil queue:NULL];
            if (capture.session.isRunning) {
                [capture.session stopRunning];
            }
            dispatch_sync(capture.sampleQueue, ^{});
            [capture removeObservers];
            callbacksDrained = dispatch_group_wait(capture.callbackGroup, deadline) == 0;
            dispatch_semaphore_signal(finished);
        });
        if (dispatch_semaphore_wait(finished, deadline) != 0 || !callbacksDrained) {
            if (error_message != NULL) {
                *error_message = InlaidCopyString(@"AVFoundation did not drain capture callbacks before the shutdown deadline");
            }
            return -1;
        }
        capture.started = NO;
        __unused InlaidAVFCapture *releasedCapture = (__bridge_transfer InlaidAVFCapture *)capture_pointer;
        return 0;
    }
}

void inlaid_avf_frame_release(void *frame_pointer) {
    if (frame_pointer == NULL) {
        return;
    }
    InlaidAVFFrame *frame = frame_pointer;
    if (frame->pixelBuffer != NULL) {
        CVPixelBufferUnlockBaseAddress(frame->pixelBuffer, kCVPixelBufferLock_ReadOnly);
        CFRelease(frame->pixelBuffer);
    }
    free(frame);
}

void inlaid_avf_free_string(char *value) {
    free(value);
}
