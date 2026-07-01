#import <AppKit/AppKit.h>
#import <CoreGraphics/CoreGraphics.h>
#import "mediakey_darwin.h"
#import "_cgo_export.h"

// System-defined event type + aux-button subtype (from IOKit/hidsystem), the
// channel the keyboard's media transport keys arrive on.
#define NX_SYSDEFINED 14
#define NX_SUBTYPE_AUX_CONTROL_BUTTONS 8

// A single process-wide tap. Written on the run-loop thread before it blocks;
// gLoop is read by lp10StopTap from another thread (CFRunLoopStop is safe cross-
// thread).
static CFMachPortRef      gTap  = NULL;
static CFRunLoopSourceRef gSrc  = NULL;
static CFRunLoopRef       gLoop = NULL;

static CGEventRef tapCallback(CGEventTapProxy proxy, CGEventType type,
                              CGEventRef event, void *refcon) {
    // The system disables a tap that times out or is disabled by user input;
    // re-enable and let the event through.
    if (type == kCGEventTapDisabledByTimeout ||
        type == kCGEventTapDisabledByUserInput) {
        if (gTap) CGEventTapEnable(gTap, true);
        return event;
    }
    if (type != NX_SYSDEFINED) {
        return event;
    }
    NSEvent *e = [NSEvent eventWithCGEvent:event];
    if (e == nil || [e subtype] != NX_SUBTYPE_AUX_CONTROL_BUTTONS) {
        return event;
    }
    long data1   = [e data1];
    int  keyCode = (int)((data1 & 0xFFFF0000) >> 16);
    int  keyState = (int)((data1 & 0x0000FF00) >> 8);
    // goMediaKey applies the shared classify/decide logic (Go) and returns 1 to
    // consume the event, 0 to pass it through.
    if (goMediaKey(keyCode, keyState) != 0) {
        return NULL;
    }
    return event;
}

int lp10InstallTap(void) {
    CGEventMask mask = CGEventMaskBit(NX_SYSDEFINED);
    gTap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap,
                            kCGEventTapOptionDefault, mask, tapCallback, NULL);
    if (gTap == NULL) {
        return 0; // almost always: Accessibility permission not granted
    }
    gSrc  = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, gTap, 0);
    gLoop = CFRunLoopGetCurrent();
    CFRunLoopAddSource(gLoop, gSrc, kCFRunLoopCommonModes);
    CGEventTapEnable(gTap, true);
    return 1;
}

void lp10RunLoop(void) {
    CFRunLoopRun(); // blocks until CFRunLoopStop (lp10StopTap)
}

void lp10StopTap(void) {
    if (gTap)  CGEventTapEnable(gTap, false);
    if (gLoop) CFRunLoopStop(gLoop);
}
