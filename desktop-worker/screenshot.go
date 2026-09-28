//go:build windows

package main

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"log"
	"time"
	"unsafe"
)

// Screen capture via CreateDIBSection instead of BitBlt+GetDIBits.
//
// Why not github.com/kbinani/screenshot (or any GetDIBits path): that
// library extracts pixels with GetDIBits while the bitmap is still selected
// into the memory DC, which MSDN forbids; some drivers reject it and
// GetDIBits returns 0 ("GetDIBits failed"). CreateDIBSection hands us a
// direct pointer to device-independent pixel memory, so we BitBlt straight
// into readable memory and never call GetDIBits.
//
// Primary display only (origin 0,0). The process is made DPI-aware in
// dpi.go before this runs, so GetSystemMetrics returns real pixels.

var (
	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procGdiFlush           = gdi32.NewProc("GdiFlush")
)

const (
	srcCopy      = 0x00CC0020
	dibRGBColors = 0
	biRGBConst   = 0
	smCyScreen   = 1
	// smCxScreen (0) is defined in overlay.go — reused here.
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// screenSize returns the primary display's size in real pixels (the
// process is DPI-aware), or 0,0 if it can't be read.
func screenSize() (w, h int) {
	width, _, _ := procGetSystemMetrics.Call(uintptr(smCxScreen))
	height, _, _ := procGetSystemMetrics.Call(uintptr(smCyScreen))
	return int(int32(width)), int(int32(height))
}

// captureImage grabs the primary display into an *image.RGBA. This is the
// raw capture; PNG encoding and stability polling are layered on top.
func captureImage() (*image.RGBA, error) {
	width, _, _ := procGetSystemMetrics.Call(uintptr(smCxScreen))
	height, _, _ := procGetSystemMetrics.Call(uintptr(smCyScreen))
	w := int32(width)
	h := int32(height)
	if w <= 0 || h <= 0 {
		return nil, errors.New("invalid screen dimensions from GetSystemMetrics")
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, errors.New("GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, errors.New("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	hdr := bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       w,
		Height:      -h, // top-down
		Planes:      1,
		BitCount:    32,
		Compression: biRGBConst,
	}
	var bits unsafe.Pointer
	bitmap, _, _ := procCreateDIBSection.Call(
		memDC,
		uintptr(unsafe.Pointer(&hdr)),
		uintptr(dibRGBColors),
		uintptr(unsafe.Pointer(&bits)),
		0, 0,
	)
	if bitmap == 0 || bits == nil {
		return nil, errors.New("CreateDIBSection failed")
	}
	defer procDeleteObject.Call(bitmap)

	old, _, _ := procSelectObject.Call(memDC, bitmap)
	if old == 0 {
		return nil, errors.New("SelectObject failed")
	}
	defer procSelectObject.Call(memDC, old)

	ret, _, _ := procBitBlt.Call(
		memDC, 0, 0, uintptr(w), uintptr(h),
		screenDC, 0, 0, uintptr(srcCopy),
	)
	if ret == 0 {
		return nil, errors.New("BitBlt failed")
	}
	procGdiFlush.Call()

	total := int(w) * int(h) * 4
	src := unsafe.Slice((*byte)(bits), total)
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for i := 0; i < total; i += 4 {
		img.Pix[i] = src[i+2]   // R
		img.Pix[i+1] = src[i+1] // G
		img.Pix[i+2] = src[i]   // B
		img.Pix[i+3] = 255      // A
	}
	return img, nil
}

// encode wraps an *image.RGBA as a PNG Screenshot payload.
func encode(img *image.RGBA) (Screenshot, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return Screenshot{}, err
	}
	b := img.Bounds()
	return Screenshot{
		Format: "png",
		Width:  uint32(b.Dx()),
		Height: uint32(b.Dy()),
		Data:   buf.Bytes(),
	}, nil
}

// CaptureScreen grabs the primary display and encodes it as PNG (a single
// instantaneous frame, no settling).
func CaptureScreen() (Screenshot, error) {
	img, err := captureImage()
	if err != nil {
		return Screenshot{}, err
	}
	return encode(img)
}

// --- Screen-settle detection -------------------------------------------
//
// The problem this solves: after an instruction's actions run (e.g. typing
// a URL and pressing Enter), the screenshot that drives the NEXT step used
// to be captured immediately — before the page/app had rendered. The AI
// then reasoned about a half-loaded screen and clicked things that weren't
// there yet. CaptureStableScreen waits until the screen stops changing
// (adaptively: fast for snappy UI, longer for slow loads) before capturing,
// so every screenshot the AI sees is of a SETTLED screen.

const (
	// Downsample stride for the visual fingerprint — every Nth pixel in
	// both dimensions is sampled. Coarse is fine; we only need to know
	// whether the screen is broadly still changing.
	fpSampleStride = 16
	// A sampled channel counts as "changed" if it moves more than this,
	// so tiny anti-aliasing/cursor-blink jitter doesn't count as motion.
	fpChannelDelta = 12
	// The screen is considered settled when fewer than this fraction of
	// sampled channels changed between two consecutive frames.
	fpStableFraction = 0.004 // 0.4%
)

// fingerprint samples the image on a coarse grid into a compact byte slice.
func fingerprint(img *image.RGBA) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, 0, (w/fpSampleStride+1)*(h/fpSampleStride+1)*3)
	for y := 0; y < h; y += fpSampleStride {
		row := y * img.Stride
		for x := 0; x < w; x += fpSampleStride {
			i := row + x*4
			out = append(out, img.Pix[i], img.Pix[i+1], img.Pix[i+2])
		}
	}
	return out
}

// changedFraction returns the fraction of fingerprint channels that differ
// by more than fpChannelDelta. Mismatched lengths (resolution change) count
// as fully changed.
func changedFraction(a, b []byte) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 1
	}
	changed := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		if d > fpChannelDelta {
			changed++
		}
	}
	return float64(changed) / float64(len(a))
}

// CaptureStableScreen waits for the screen to settle, then returns a PNG of
// the settled frame. It sleeps `initial` first (to let a load actually
// begin), then polls every `poll` until two consecutive frames are
// near-identical, or `maxWait` elapses — whichever comes first. On timeout
// it returns the most recent frame (best effort) rather than failing.
func CaptureStableScreen(initial, poll, maxWait time.Duration) (Screenshot, error) {
	start := time.Now()
	if initial > 0 {
		time.Sleep(initial)
	}

	img, err := captureImage()
	if err != nil {
		return Screenshot{}, err
	}
	prev := fingerprint(img)

	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		time.Sleep(poll)
		next, err := captureImage()
		if err != nil {
			continue // transient capture hiccup; try again
		}
		nfp := fingerprint(next)
		frac := changedFraction(prev, nfp)
		img = next
		prev = nfp
		if frac < fpStableFraction {
			log.Printf("screenshot: screen settled after %v", time.Since(start).Round(time.Millisecond))
			return encode(img)
		}
	}

	log.Printf("screenshot: screen did NOT settle within %v (still changing) — capturing anyway", maxWait)
	return encode(img)
}
