#import <Foundation/Foundation.h>
#import <Vision/Vision.h>
#import <CoreGraphics/CoreGraphics.h>
#include "vision_ocr.h"
#include <stdlib.h>
#include <string.h>

int ss_vision_ocr(
    const uint8_t *bgra,
    int width,
    int height,
    int stride,
    ss_ocr_rect *rects,
    char **texts,
    int max_out
) {
    if (!bgra || !rects || !texts || width <= 0 || height <= 0 || max_out <= 0) return -1;

    size_t nbytes = (size_t)stride * (size_t)height;
    CFDataRef data = CFDataCreate(kCFAllocatorDefault, bgra, (CFIndex)nbytes);
    if (!data) return -1;

    CGDataProviderRef provider = CGDataProviderCreateWithCFData(data);
    CGColorSpaceRef cs = CGColorSpaceCreateDeviceRGB();
    CGBitmapInfo info = kCGBitmapByteOrder32Little | kCGImageAlphaPremultipliedFirst; /* BGRA */
    CGImageRef image = CGImageCreate(
        (size_t)width, (size_t)height,
        8, 32, (size_t)stride,
        cs, info, provider, NULL, false, kCGRenderingIntentDefault
    );
    CGColorSpaceRelease(cs);
    CGDataProviderRelease(provider);
    CFRelease(data);
    if (!image) return -1;

    __block int count = 0;
    VNImageRequestHandler *handler = [[VNImageRequestHandler alloc] initWithCGImage:image options:@{}];
    VNRecognizeTextRequest *req = [[VNRecognizeTextRequest alloc] initWithCompletionHandler:^(VNRequest *request, NSError *error) {
        if (error || count >= max_out) return;
        for (VNRecognizedTextObservation *obs in request.results) {
            if (count >= max_out) break;
            VNRecognizedText *top = [[obs topCandidates:1] firstObject];
            if (!top) continue;
            CGRect bb = obs.boundingBox; /* Vision: bottom-left origin, normalized */
            int x = (int)(bb.origin.x * width);
            int y = (int)((1.0 - bb.origin.y - bb.size.height) * height);
            int w = (int)(bb.size.width * width);
            int h = (int)(bb.size.height * height);
            if (x < 0) x = 0;
            if (y < 0) y = 0;
            if (x + w > width) w = width - x;
            if (y + h > height) h = height - y;
            if (w <= 0 || h <= 0) continue;
            rects[count].x = (uint16_t)x;
            rects[count].y = (uint16_t)y;
            rects[count].w = (uint16_t)w;
            rects[count].h = (uint16_t)h;
            const char *utf8 = [top.string UTF8String];
            texts[count] = utf8 ? strdup(utf8) : strdup("");
            count++;
        }
    }];
    req.recognitionLevel = VNRequestTextRecognitionLevelFast;

    NSError *err = nil;
    BOOL ok = [handler performRequests:@[req] error:&err];
    CGImageRelease(image);
    if (!ok) return -1;
    return count;
}
