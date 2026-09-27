//go:build windows

package main

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"unsafe"
)

// Screen capture via CreateDIBSection instead of BitBlt+GetDIBits.
//
// Why we don't use github.com/kbinani/screenshot (or any GetDIBits-based
// path): that library captures into a device-dependent bitmap (DDB) with
// CreateCompatibleBitmap, then extracts the pixels with GetDIBits WHILE
// the bitmap is still selected into the memory DC. MSDN is explicit that
// "the bitmap identified by the hbmp parameter must not be selected into a
// device context when the application calls this function." Most GPU
// drivers tolerate the violation; some reject it, and GetDIBits then just
// returns 0 with no useful error — which surfaced here as a hard,
// first-call, every-time "GetDIBits failed" (BitBlt itself succeeded, so
// the capture worked; only the DDB->DIB extraction failed).
//
// CreateDIBSection sidesteps the whole problem: it allocates a
// device-INDEPENDENT bitmap whose pixel memory we get a direct pointer to,
// so we BitBlt straight into readable memory and never call GetDIBits at
// all. This is the approach MSDN's own "Capturing an Image" guidance and
// long-standing GDI advice both point to for exactly this failure.
//
// Primary display only (origin 0,0), matching the previous behavior. The
// process is made DPI-aware in dpi.go before this ever runs, so
// GetSystemMetrics returns real pixel dimensions.

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
	// smCxScreen (0) is already defined in overlay.go — reused here.
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

// CaptureScreen grabs the primary display and encodes it as PNG.
func CaptureScreen() (Screenshot, error) {
	width, _, _ := procGetSystemMetrics.Call(uintptr(smCxScreen))
	height, _, _ := procGetSystemMetrics.Call(uintptr(smCyScreen))
	w := int32(width)
	h := int32(height)
	if w <= 0 || h <= 0 {
		return Screenshot{}, errors.New("invalid screen dimensions from GetSystemMetrics")
	}

	// NULL hwnd => a DC for the entire primary screen.
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return Screenshot{}, errors.New("GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return Screenshot{}, errors.New("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	hdr := bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       w,
		Height:      -h, // negative height => top-down rows, matching image.RGBA
		Planes:      1,
		BitCount:    32,
		Compression: biRGBConst,
	}

	// bits receives a pointer into the DIB's pixel memory — we read
	// straight from it after the blit, no GetDIBits round trip.
	var bits unsafe.Pointer
	bitmap, _, _ := procCreateDIBSection.Call(
		memDC,
		uintptr(unsafe.Pointer(&hdr)),
		uintptr(dibRGBColors),
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if bitmap == 0 || bits == nil {
		return Screenshot{}, errors.New("CreateDIBSection failed")
	}
	defer procDeleteObject.Call(bitmap)

	old, _, _ := procSelectObject.Call(memDC, bitmap)
	if old == 0 {
		return Screenshot{}, errors.New("SelectObject failed")
	}
	defer procSelectObject.Call(memDC, old)

	ret, _, _ := procBitBlt.Call(
		memDC, 0, 0, uintptr(w), uintptr(h),
		screenDC, 0, 0, uintptr(srcCopy),
	)
	if ret == 0 {
		return Screenshot{}, errors.New("BitBlt failed")
	}

	// GDI batches drawing calls; flush so the blit is guaranteed complete
	// before we read the DIB memory directly.
	procGdiFlush.Call()

	// DIB memory is BGRA, top-down (negative height above). Convert to the
	// RGBA layout image.RGBA expects.
	total := int(w) * int(h) * 4
	src := unsafe.Slice((*byte)(bits), total)
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for i := 0; i < total; i += 4 {
		img.Pix[i] = src[i+2]   // R <- B-position byte order (BGRA -> RGBA)
		img.Pix[i+1] = src[i+1] // G
		img.Pix[i+2] = src[i]   // B
		img.Pix[i+3] = 255      // A (screen has no meaningful alpha)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return Screenshot{}, err
	}

	return Screenshot{
		Format: "png",
		Width:  uint32(w),
		Height: uint32(h),
		Data:   buf.Bytes(),
	}, nil
}
