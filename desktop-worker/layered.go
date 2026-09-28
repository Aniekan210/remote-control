//go:build windows

package main

import (
	"image"
	"log"
	"syscall"
	"unsafe"
)

// layeredWindow is one click-through, always-on-top, per-pixel-alpha
// window (UpdateLayeredWindow / ULW_ALPHA) that shows an image.RGBA. The
// overlay is made of five of them: four thin edge strips for the glow and
// the status pill (see overlay.go). Splitting the glow into strips means
// only the band around the edge is ever redrawn and pushed — never a full
// screen-sized buffer — which is what makes animating it cheap.
//
// Every window excludes itself from screen capture
// (WDA_EXCLUDEFROMCAPTURE), so none of it appears in the screenshots sent
// to the AI.
//
// NOTE: standard but unverified-on-hardware Win32 — build and eyeball it.
// REMOTE_WORKER_NO_OVERLAY=1 disables the overlay entirely.

const (
	ulwAlpha   = 0x02
	acSrcOver  = 0x00
	acSrcAlpha = 0x01
)

type blendFunction struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type sizeT struct{ CX, CY int32 }

var procUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")

type layeredWindow struct {
	hwnd    syscall.Handle
	memDC   uintptr
	bitmap  uintptr
	pix     []byte // the DIB's BGRA pixels (premultiplied)
	img     *image.RGBA
	x, y    int32
	w, h    int32
	shown   bool
	opacity byte // last SourceConstantAlpha pushed
}

// overlayClassName is registered once (registerOverlayClass) and shared by
// every overlay window.
var overlayClassName, _ = syscall.UTF16PtrFromString("RemoteWorkerOverlayClass")

func registerOverlayClass(hInstance uintptr, wndProc uintptr) bool {
	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   wndProc,
		hInstance:     syscall.Handle(hInstance),
		lpszClassName: overlayClassName,
	}
	if ret, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		log.Printf("overlay: RegisterClassExW failed: %v", err)
		return false
	}
	return true
}

// newLayeredWindow creates a hidden overlay window covering r (screen
// coordinates) with a matching premultiplied-alpha DIB. nil on failure.
func newLayeredWindow(hInstance uintptr, r image.Rectangle) *layeredWindow {
	w, h := int32(r.Dx()), int32(r.Dy())
	if w <= 0 || h <= 0 {
		return nil
	}
	name, _ := syscall.UTF16PtrFromString("RemoteWorkerOverlay")
	hwndRaw, _, err := procCreateWindowExW.Call(
		uintptr(wsExLayered|wsExTransparent|wsExTopmost|wsExToolWindow|wsExNoActivate),
		uintptr(unsafe.Pointer(overlayClassName)),
		uintptr(unsafe.Pointer(name)),
		uintptr(wsPopup),
		uintptr(r.Min.X), uintptr(r.Min.Y), uintptr(w), uintptr(h),
		0, 0, hInstance, 0,
	)
	if hwndRaw == 0 {
		log.Printf("overlay: CreateWindowExW failed: %v", err)
		return nil
	}
	hwnd := syscall.Handle(hwndRaw)
	procSetWindowDisplayAffinity.Call(uintptr(hwnd), uintptr(wdaExcludeFromCapture))

	screenDC, _, _ := procGetDC.Call(0)
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	procReleaseDC.Call(0, screenDC)

	hdr := bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       w,
		Height:      -h, // top-down
		Planes:      1,
		BitCount:    32,
		Compression: biRGBConst,
	}
	var bits unsafe.Pointer
	bitmap, _, _ := procCreateDIBSection.Call(memDC, uintptr(unsafe.Pointer(&hdr)), uintptr(dibRGBColors),
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		log.Printf("overlay: CreateDIBSection failed")
		procDeleteDC.Call(memDC)
		return nil
	}
	// Stays selected for the window's lifetime: UpdateLayeredWindow reads
	// from memDC on every push.
	procSelectObject.Call(memDC, bitmap)

	return &layeredWindow{
		hwnd:   hwnd,
		memDC:  memDC,
		bitmap: bitmap,
		pix:    unsafe.Slice((*byte)(bits), int(w)*int(h)*4),
		img:    image.NewRGBA(image.Rect(0, 0, int(w), int(h))),
		x:      int32(r.Min.X),
		y:      int32(r.Min.Y),
		w:      w,
		h:      h,
	}
}

// push copies img into the window (RGBA -> BGRA, both premultiplied) and
// shows it at the given overall opacity.
func (lw *layeredWindow) push(opacity byte) {
	src := lw.img.Pix
	for i := 0; i+3 < len(src); i += 4 {
		lw.pix[i], lw.pix[i+1], lw.pix[i+2], lw.pix[i+3] = src[i+2], src[i+1], src[i], src[i+3]
	}
	lw.update(opacity)
}

// setOpacity re-shows the current contents at a new overall opacity,
// without touching the pixels (fades are just this).
func (lw *layeredWindow) setOpacity(opacity byte) {
	if opacity != lw.opacity || !lw.shown {
		lw.update(opacity)
	}
}

func (lw *layeredWindow) update(opacity byte) {
	lw.opacity = opacity
	if opacity == 0 {
		lw.hide()
		return
	}
	ptSrc := point{0, 0}
	ptDst := point{lw.x, lw.y}
	sz := sizeT{lw.w, lw.h}
	blend := blendFunction{BlendOp: acSrcOver, SourceConstantAlpha: opacity, AlphaFormat: acSrcAlpha}
	screenDC, _, _ := procGetDC.Call(0)
	procUpdateLayeredWindow.Call(uintptr(lw.hwnd), screenDC,
		uintptr(unsafe.Pointer(&ptDst)), uintptr(unsafe.Pointer(&sz)),
		lw.memDC, uintptr(unsafe.Pointer(&ptSrc)), 0,
		uintptr(unsafe.Pointer(&blend)), uintptr(ulwAlpha))
	procReleaseDC.Call(0, screenDC)
	if !lw.shown {
		procShowWindow.Call(uintptr(lw.hwnd), uintptr(swShowNoActivate))
		lw.shown = true
	}
}

func (lw *layeredWindow) hide() {
	if lw.shown {
		procShowWindow.Call(uintptr(lw.hwnd), uintptr(swHide))
		lw.shown = false
	}
}

// contains reports whether a screen point is inside r, given in the
// window's own coordinates.
func (lw *layeredWindow) contains(p point, r image.Rectangle) bool {
	x, y := int(p.X-lw.x), int(p.Y-lw.y)
	return image.Pt(x, y).In(r)
}

func (lw *layeredWindow) destroy() {
	procDeleteDC.Call(lw.memDC)
	procDeleteObject.Call(lw.bitmap)
}
