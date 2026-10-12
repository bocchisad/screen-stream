// macOS ScreenCaptureKit capture helper (BGRA → caller buffer).
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <Foundation/Foundation.h>
#import <ApplicationServices/ApplicationServices.h>
#import <CoreGraphics/CoreGraphics.h>
#import <AppKit/AppKit.h>
#include "helper.h"
#include <string.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>

static void ss_native_size(SCDisplay *display, int *outW, int *outH) {
    CGDirectDisplayID did = display.displayID;
    int pw = (int)CGDisplayPixelsWide(did);
    int ph = (int)CGDisplayPixelsHigh(did);
    int dw = (int)display.width;
    int dh = (int)display.height;
    /* Prefer NSScreen backingScaleFactor (Retina looking-glass → physical pixels). */
    for (NSScreen *s in [NSScreen screens]) {
        NSDictionary *desc = s.deviceDescription;
        NSNumber *num = desc[@"NSScreenNumber"];
        if (num && [num unsignedIntValue] == did) {
            CGFloat bs = s.backingScaleFactor;
            if (bs < 1.0) bs = 1.0;
            int sw = (int)llround((double)dw * (double)bs);
            int sh = (int)llround((double)dh * (double)bs);
            if (sw > pw) pw = sw;
            if (sh > ph) ph = sh;
            break;
        }
    }
    if (pw <= 0) pw = dw;
    if (ph <= 0) ph = dh;
    *outW = pw;
    *outH = ph;
}

static atomic_int gFrameReady = 0;

@interface SSStreamDelegate : NSObject <SCStreamOutput, SCStreamDelegate>
@property (nonatomic, assign) uint8_t *latest;
@property (nonatomic, assign) size_t latestLen;
@property (nonatomic, assign) int width;
@property (nonatomic, assign) int height;
@property (nonatomic, assign) int stride;
@property (nonatomic, strong) NSLock *lock;
@end

@implementation SSStreamDelegate
- (instancetype)init {
    if ((self = [super init])) {
        _lock = [[NSLock alloc] init];
        atomic_store(&gFrameReady, 0);
    }
    return self;
}

- (void)stream:(SCStream *)stream
 didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
                ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeScreen) return;
    CVImageBufferRef imageBuffer = CMSampleBufferGetImageBuffer(sampleBuffer);
    if (!imageBuffer) return;

    CVPixelBufferLockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);
    size_t w = CVPixelBufferGetWidth(imageBuffer);
    size_t h = CVPixelBufferGetHeight(imageBuffer);
    size_t stride = CVPixelBufferGetBytesPerRow(imageBuffer);
    void *base = CVPixelBufferGetBaseAddress(imageBuffer);
    if (w == 0 || h == 0 || stride == 0 || stride > (SIZE_MAX / h)) {
        CVPixelBufferUnlockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);
        return;
    }
    size_t need = stride * h;
    /* Cap ~64 MiB single frame */
    if (need > (64u * 1024u * 1024u)) {
        CVPixelBufferUnlockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);
        return;
    }

    [self.lock lock];
    if (!self.latest || self.latestLen < need) {
        free(self.latest);
        self.latest = (uint8_t *)malloc(need);
        self.latestLen = self.latest ? need : 0;
    }
    if (self.latest && base) {
        memcpy(self.latest, base, need);
        self.width = (int)w;
        self.height = (int)h;
        self.stride = (int)stride;
        atomic_store(&gFrameReady, 1);
    }
    [self.lock unlock];
    CVPixelBufferUnlockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);
}

- (void)dealloc {
    [_lock lock];
    free(_latest);
    _latest = NULL;
    _latestLen = 0;
    [_lock unlock];
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    (void)stream;
    (void)error;
}
@end

static SCStream *gStream = nil;
static SSStreamDelegate *gDelegate = nil;
static NSMutableArray<NSString *> *gExcludeBundles = nil;
static NSMutableArray<NSNumber *> *gExcludeWindows = nil;
static int gOutW = 0, gOutH = 0, gOutStride = 0;

int ss_mac_preflight(void) {
    if (@available(macOS 14.0, *)) {
        return CGPreflightScreenCaptureAccess() ? 0 : 1;
    }
    return 0;
}

int ss_mac_request_permission(void) {
    if (@available(macOS 14.0, *)) {
        return CGRequestScreenCaptureAccess() ? 0 : 1;
    }
    return 0;
}

int ss_mac_set_exclude_bundle(const char *bundle_id) {
    if (!bundle_id) return 3;
    if (!gExcludeBundles) gExcludeBundles = [NSMutableArray array];
    [gExcludeBundles addObject:[NSString stringWithUTF8String:bundle_id]];
    return 0;
}

int ss_mac_set_exclude_window(uint32_t window_id) {
    if (!gExcludeWindows) gExcludeWindows = [NSMutableArray array];
    [gExcludeWindows addObject:@(window_id)];
    return 0;
}

int ss_mac_clear_excludes(void) {
    [gExcludeBundles removeAllObjects];
    [gExcludeWindows removeAllObjects];
    return 0;
}

int ss_mac_display_pixels(int display_index, int *out_w, int *out_h) {
    if (!out_w || !out_h) return 3;
    __block int result = 3;
    __block SCShareableContent *content = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent *c, NSError *err) {
        if (err || !c || c.displays.count == 0) result = 2;
        else { content = c; result = 0; }
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
    if (result != 0) return result;
    if (display_index < 0 || display_index >= (int)content.displays.count) display_index = 0;
    SCDisplay *display = content.displays[(NSUInteger)display_index];
    ss_native_size(display, out_w, out_h);
    return 0;
}

int ss_mac_start(int display_index, int fps) {
    return ss_mac_start_ex(display_index, fps, 100, 0, 0);
}

int ss_mac_start_ex(int display_index, int fps, int scale_percent, int max_w, int max_h) {
    if (ss_mac_preflight() != 0) {
        if (ss_mac_request_permission() != 0) return 1;
    }
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    __block int result = 3;
    __block SCShareableContent *content = nil;

    [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent *c, NSError *err) {
        if (err || !c || c.displays.count == 0) {
            result = (err && err.code == SCStreamErrorUserDeclined) ? 1 : 2;
        } else {
            content = c;
            result = 0;
        }
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
    if (result != 0) return result;

    if (display_index < 0 || display_index >= (int)content.displays.count) display_index = 0;
    SCDisplay *display = content.displays[(NSUInteger)display_index];

    NSMutableArray<SCRunningApplication *> *excludeApps = [NSMutableArray array];
    if (gExcludeBundles.count > 0) {
        for (SCRunningApplication *app in content.applications) {
            if ([gExcludeBundles containsObject:app.bundleIdentifier]) {
                [excludeApps addObject:app];
            }
        }
    }

    NSMutableArray<SCWindow *> *excludeWins = [NSMutableArray array];
    if (gExcludeWindows.count > 0 || excludeApps.count > 0) {
        NSSet<NSNumber *> *wantIDs = gExcludeWindows.count > 0
            ? [NSSet setWithArray:gExcludeWindows] : nil;
        NSSet<SCRunningApplication *> *wantApps = excludeApps.count > 0
            ? [NSSet setWithArray:excludeApps] : nil;
        NSMutableSet<NSString *> *wantBundles = nil;
        if (wantApps) {
            wantBundles = [NSMutableSet set];
            for (SCRunningApplication *app in wantApps) {
                if (app.bundleIdentifier) [wantBundles addObject:app.bundleIdentifier];
            }
        }
        for (SCWindow *win in content.windows) {
            BOOL drop = NO;
            if (wantIDs && [wantIDs containsObject:@(win.windowID)]) drop = YES;
            if (!drop && wantBundles && win.owningApplication.bundleIdentifier &&
                [wantBundles containsObject:win.owningApplication.bundleIdentifier]) drop = YES;
            if (drop) [excludeWins addObject:win];
        }
    }

    SCContentFilter *filter = nil;
    if (excludeWins.count > 0) {
        filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:excludeWins];
    } else {
        filter = [[SCContentFilter alloc] initWithDisplay:display
                                  excludingApplications:excludeApps
                                       exceptingWindows:@[]];
    }

    /* Prefer physical pixels (Retina), then optional scale / caps. */
    int pw = 0, ph = 0;
    ss_native_size(display, &pw, &ph);
    if (scale_percent <= 0) scale_percent = 100;
    int outW = (int)((long long)pw * scale_percent / 100);
    int outH = (int)((long long)ph * scale_percent / 100);
    if (outW < 2) outW = 2;
    if (outH < 2) outH = 2;
    if (max_w > 0 && outW > max_w) {
        outH = (int)((long long)outH * max_w / outW);
        outW = max_w;
    }
    if (max_h > 0 && outH > max_h) {
        outW = (int)((long long)outW * max_h / outH);
        outH = max_h;
    }

    SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
    cfg.width = outW;
    cfg.height = outH;
    cfg.scalesToFit = YES;
    cfg.pixelFormat = kCVPixelFormatType_32BGRA;
    cfg.capturesAudio = NO;
    if (fps <= 0) fps = 60;
    cfg.minimumFrameInterval = CMTimeMake(1, fps);
    cfg.queueDepth = 3;

    gOutW = outW;
    gOutH = outH;
    /* CVPixelBuffer rows are often 64-byte aligned — pad until first frame updates. */
    gOutStride = ((outW * 4) + 63) & ~63;

    gDelegate = [[SSStreamDelegate alloc] init];
    gStream = [[SCStream alloc] initWithFilter:filter configuration:cfg delegate:gDelegate];

    NSError *addErr = nil;
    [gStream addStreamOutput:gDelegate
                        type:SCStreamOutputTypeScreen
          sampleHandlerQueue:dispatch_get_global_queue(QOS_CLASS_USER_INTERACTIVE, 0)
                       error:&addErr];
    if (addErr) return 2;

    dispatch_semaphore_t startSem = dispatch_semaphore_create(0);
    __block int startResult = 0;
    [gStream startCaptureWithCompletionHandler:^(NSError *error) {
        if (error) startResult = (error.code == SCStreamErrorUserDeclined) ? 1 : 2;
        dispatch_semaphore_signal(startSem);
    }];
    dispatch_semaphore_wait(startSem, DISPATCH_TIME_FOREVER);
    return startResult;
}

int ss_mac_output_size(int *out_w, int *out_h, int *out_stride) {
    if (!out_w || !out_h || !out_stride) return 3;
    if (gOutW <= 0 || gOutH <= 0) return 2;
    *out_w = gOutW;
    *out_h = gOutH;
    *out_stride = gOutStride > 0 ? gOutStride : gOutW * 4;
    return 0;
}

int ss_mac_stop(void) {
    if (!gStream) return 0;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [gStream stopCaptureWithCompletionHandler:^(NSError *error) {
        (void)error;
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
    gStream = nil;
    /* ARC dealloc frees latest via -dealloc */
    gDelegate = nil;
    gOutW = gOutH = gOutStride = 0;
    atomic_store(&gFrameReady, 0);
    return 0;
}

int ss_mac_capture(uint8_t *dst, int dst_len, ss_frame_info *info) {
    if (!gDelegate || !dst || !info || dst_len <= 0) return 3;
    if (!atomic_load(&gFrameReady)) return 4;
    [gDelegate.lock lock];
    int w = gDelegate.width;
    int h = gDelegate.height;
    int stride = gDelegate.stride;
    if (w <= 0 || h <= 0 || stride <= 0) {
        [gDelegate.lock unlock];
        return 3;
    }
    if ((size_t)stride > SIZE_MAX / (size_t)h) {
        [gDelegate.lock unlock];
        return 3;
    }
    size_t need = (size_t)stride * (size_t)h;
    if (need > (size_t)dst_len || !gDelegate.latest) {
        [gDelegate.lock unlock];
        return 3;
    }
    memcpy(dst, gDelegate.latest, need);
    atomic_store(&gFrameReady, 0);
    [gDelegate.lock unlock];
    info->width = w;
    info->height = h;
    info->stride = stride;
    gOutW = w;
    gOutH = h;
    gOutStride = stride;
    return 0;
}
