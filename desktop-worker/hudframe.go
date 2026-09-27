//go:build windows

package main

import (
	"log"
	"syscall"
	"unsafe"
)

// HUDFrame is a full-screen, click-through, per-pixel-alpha overlay window
// that draws a glowing border around the entire display plus corner
// targeting brackets — a heads-up "the machine is driving" frame rather
// than a floating box. The edge glow fades inward so the center of the
// screen stays completely clear and usable; only the perimeter lights up.
//
// It's a separate window from the status panel (overlay.go) for one
// concrete reason: this window uses UpdateLayeredWindow / ULW_ALPHA, which
// needs a premultiplied-alpha bitmap and gives full per-pixel control (so
// the glow can actually fade). GDI text drawn onto such a window comes out
// invisible (GDI writes RGB but leaves alpha at 0), which is exactly why
// the readable step text lives on the LWA_ALPHA panel instead, where GDI
// text renders normally. Two windows, each using the layering mode that
// suits what it draws.
//
// Both windows set WDA_EXCLUDEFROMCAPTURE, so neither appears in the
// screenshots sent to the AI.
//
// Cost note: the frame bitmap is only redrawn on a status change (RUNNING
// -> PAUSED -> COMPLETED), never on a timer — there's no animation loop
// eating CPU, in keeping with the "lowest footprint possible" requirement.
// A full-screen buffer is repainted a handful of times per task, not per
// frame.
//
// NOTE: like the rest of the overlay, this is standard but unverified-on-
// hardware Win32 — build and eyeball before the demo.

const (
	ulwAlpha   = 0x02
	acSrcOver  = 0x00
	acSrcAlpha = 0x01

	// Frame geometry — strong neon HUD: a bold inward glow, a bright crisp
	// edge line, and prominent corner brackets. This is the whole overlay
	// now (there's no status panel), so it carries all the visual weight.
	glowThickness = 150 // px the glow reaches inward (broad, luminous wash)
	glowMaxAlpha  = 130 // peak glow alpha at the very edge (strong)
	edgeLineWidth = 3   // crisp bright line right at the screen edge
	edgeLineAlpha = 235 // bold, well-defined frame
	bracketLength = 170 // long, confident corner brackets
	bracketThick  = 5   // thick arms
	bracketAlpha  = 255 // fully opaque — the brightest accent
)

type blendFunction struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type sizeT struct{ CX, CY int32 }

var procUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")

// hudFrame is the single process-wide instance, so frameWndProc (which
// Win32 calls with only an HWND) can reach the buffer/DC it needs to
// repaint. There is only ever one overlay per process.
var hudFrame *HUDFrame

type HUDFrame struct {
	hwnd   syscall.Handle
	memDC  uintptr
	bitmap uintptr
	pix    []byte
	w, h   int32
}

func frameWndProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmUpdate:
		if hudFrame != nil {
			overlayMu.Lock()
			status := overlayStatus
			visible := overlayVisible
			overlayMu.Unlock()
			hudFrame.render(status, visible)
		}
		return 0
	case wmDestroy:
		// The frame is the only window now, so it owns the quit. Ending the
		// message loop returns control to RunOverlay, which frees the frame's
		// resources.
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

// newHUDFrame registers the frame window class, creates the full-screen
// layered window, and allocates its premultiplied-alpha DIB. Returns nil
// (with a logged reason) on any failure — the caller then just runs with
// the status panel alone rather than crashing.
func newHUDFrame(hInstance uintptr) *HUDFrame {
	className, _ := syscall.UTF16PtrFromString("RemoteWorkerHUDFrameClass")
	windowName, _ := syscall.UTF16PtrFromString("RemoteWorkerHUD")

	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   syscall.NewCallback(frameWndProc),
		hInstance:     syscall.Handle(hInstance),
		lpszClassName: className,
	}
	if ret, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		log.Printf("hudframe: RegisterClassExW failed: %v", err)
		return nil
	}

	sw, _, _ := procGetSystemMetrics.Call(uintptr(smCxScreen))
	sh, _, _ := procGetSystemMetrics.Call(uintptr(smCyScreen))
	w := int32(sw)
	h := int32(sh)
	if w <= 0 || h <= 0 {
		log.Printf("hudframe: bad screen metrics %dx%d", w, h)
		return nil
	}

	hwndRaw, _, err := procCreateWindowExW.Call(
		uintptr(wsExLayered|wsExTransparent|wsExTopmost|wsExToolWindow|wsExNoActivate),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		uintptr(wsPopup),
		0, 0, uintptr(w), uintptr(h),
		0, 0, hInstance, 0,
	)
	hwnd := syscall.Handle(hwndRaw)
	if hwnd == 0 {
		log.Printf("hudframe: CreateWindowExW failed: %v", err)
		return nil
	}

	procSetWindowDisplayAffinity.Call(uintptr(hwnd), uintptr(wdaExcludeFromCapture))

	screenDC, _, _ := procGetDC.Call(0)
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)

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
	procReleaseDC.Call(0, screenDC)

	if bitmap == 0 || bits == nil {
		log.Printf("hudframe: CreateDIBSection failed")
		procDeleteDC.Call(memDC)
		return nil
	}

	// Keep the DIB selected for the window's lifetime — UpdateLayeredWindow
	// reads from memDC with this bitmap selected on every push.
	procSelectObject.Call(memDC, bitmap)

	return &HUDFrame{
		hwnd:   hwnd,
		memDC:  memDC,
		bitmap: bitmap,
		pix:    unsafe.Slice((*byte)(bits), int(w)*int(h)*4),
		w:      w,
		h:      h,
	}
}

func (f *HUDFrame) destroy() {
	procDeleteDC.Call(f.memDC)
	procDeleteObject.Call(f.bitmap)
}

// render repaints the frame in the status accent color and pushes it, or
// hides the window when the task isn't visible.
func (f *HUDFrame) render(status string, visible bool) {
	if !visible {
		procShowWindow.Call(uintptr(f.hwnd), uintptr(swHide))
		return
	}

	f.draw(statusColor(status))

	ptSrc := point{0, 0}
	ptDst := point{0, 0}
	sz := sizeT{f.w, f.h}
	blend := blendFunction{
		BlendOp:             acSrcOver,
		SourceConstantAlpha: 255,
		AlphaFormat:         acSrcAlpha,
	}

	screenDC, _, _ := procGetDC.Call(0)
	procUpdateLayeredWindow.Call(
		uintptr(f.hwnd),
		screenDC,
		uintptr(unsafe.Pointer(&ptDst)),
		uintptr(unsafe.Pointer(&sz)),
		f.memDC,
		uintptr(unsafe.Pointer(&ptSrc)),
		0,
		uintptr(unsafe.Pointer(&blend)),
		uintptr(ulwAlpha),
	)
	procReleaseDC.Call(0, screenDC)

	procShowWindow.Call(uintptr(f.hwnd), uintptr(swShowNoActivate))
}

// setPixelPremul writes a premultiplied BGRA pixel. UpdateLayeredWindow
// with AC_SRC_ALPHA expects each color channel already multiplied by
// alpha/255, so a fully transparent pixel is all-zero and a solid one is
// (B,G,R,255).
func setPixelPremul(pix []byte, i int, r, g, b, a byte) {
	pix[i] = byte(uint32(b) * uint32(a) / 255)
	pix[i+1] = byte(uint32(g) * uint32(a) / 255)
	pix[i+2] = byte(uint32(r) * uint32(a) / 255)
	pix[i+3] = a
}

func (f *HUDFrame) fillRectPremul(x0, y0, x1, y1 int, r, g, b, a byte) {
	w := int(f.w)
	h := int(f.h)
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > w {
		x1 = w
	}
	if y1 > h {
		y1 = h
	}
	for y := y0; y < y1; y++ {
		row := y * w
		for x := x0; x < x1; x++ {
			setPixelPremul(f.pix, (row+x)*4, r, g, b, a)
		}
	}
}

// draw repaints the whole frame buffer: transparent center, an inward-
// fading edge glow, a crisp edge line, and four corner brackets — all in
// the given accent color (0x00BBGGRR as produced by rgb()).
func (f *HUDFrame) draw(accent uint32) {
	clear(f.pix) // fully transparent everywhere to start

	r := byte(accent & 0xFF)
	g := byte((accent >> 8) & 0xFF)
	b := byte((accent >> 16) & 0xFF)

	w := int(f.w)
	h := int(f.h)

	// Edge glow: alpha falls off quadratically from each edge inward, using
	// distance to the NEAREST edge so the four corners naturally read as
	// brighter pooled light rather than seams.
	for y := 0; y < h; y++ {
		dy := y
		if h-1-y < dy {
			dy = h - 1 - y
		}
		row := y * w
		for x := 0; x < w; x++ {
			dx := x
			if w-1-x < dx {
				dx = w - 1 - x
			}
			d := dx
			if dy < d {
				d = dy
			}

			a := 0
			if d < glowThickness {
				t := float64(glowThickness-d) / float64(glowThickness)
				a = int(float64(glowMaxAlpha) * t * t)
			}
			if d < edgeLineWidth {
				a = edgeLineAlpha
			}
			if a > 0 {
				setPixelPremul(f.pix, (row+x)*4, r, g, b, byte(a))
			}
		}
	}

	// Corner brackets — the bright L's in each corner that give it the
	// "targeting HUD" read.
	bl := bracketLength
	bt := bracketThick
	// top-left
	f.fillRectPremul(0, 0, bl, bt, r, g, b, bracketAlpha)
	f.fillRectPremul(0, 0, bt, bl, r, g, b, bracketAlpha)
	// top-right
	f.fillRectPremul(w-bl, 0, w, bt, r, g, b, bracketAlpha)
	f.fillRectPremul(w-bt, 0, w, bl, r, g, b, bracketAlpha)
	// bottom-left
	f.fillRectPremul(0, h-bt, bl, h, r, g, b, bracketAlpha)
	f.fillRectPremul(0, h-bl, bt, h, r, g, b, bracketAlpha)
	// bottom-right
	f.fillRectPremul(w-bl, h-bt, w, h, r, g, b, bracketAlpha)
	f.fillRectPremul(w-bt, h-bl, w, h, r, g, b, bracketAlpha)
}
