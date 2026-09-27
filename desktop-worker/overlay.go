//go:build windows

package main

import (
	"log"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Overlay is a plain Win32 popup window used purely as a status indicator:
// no interaction, no chrome, no taskbar entry. It stays hidden until a
// task is running/paused/just-completed, and it excludes itself from
// screen captures via SetWindowDisplayAffinity so it never shows up in the
// screenshots sent to the AI (which would otherwise confuse the vision
// model into treating the overlay as part of the target application's UI).
//
// Visual style: a dark, translucent card, top-right of the screen, with —
//   - a status dot + uppercase status label + a right-aligned "N / M" step
//     counter, all on one line
//   - the current step's instruction text below that, larger and brighter
//   - a thin progress bar along the bottom tracking StepIndex/StepTotal
//
// This shows granular step-by-step progress (what instruction is actually
// running right now, and how far through the task it is) rather than just
// a static RUNNING/PAUSED word — see OverlayState in state.go for the data
// this is built from.
//
// This is built from raw Win32 calls rather than a UI framework to keep
// the binary small and dependency-free, matching the "as lightweight as
// possible" requirement.
//
// NOTE: I have not been able to compile or run this against real Windows
// hardware — the patterns here (layered window + LWA_ALPHA, WM_PAINT with
// GDI, SetWindowRgn for rounded corners, CreateFontW) are standard,
// documented Win32 usage, but budget time to build and eyeball it before
// relying on it for a demo. I deliberately did NOT use
// DwmEnableBlurBehindWindow for a "frosted glass" look — Microsoft's own
// docs state it stopped producing an actual blur starting with Windows 8
// (it now behaves like plain translucency), so it would add API surface
// and risk for zero visual gain on Windows 10/11.

const (
	wsPopup         = 0x80000000
	wsExLayered     = 0x00080000
	wsExTransparent = 0x00000020
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExNoActivate  = 0x08000000

	swHide           = 0
	swShowNoActivate = 4

	lwaAlpha = 0x2

	wmDestroy = 0x0002
	wmPaint   = 0x000F
	wmApp     = 0x8000
	wmUpdate  = wmApp + 1 // custom message: "re-read shared state and redraw"

	dtLeft            = 0x0
	dtRight           = 0x2
	dtVCenter         = 0x4
	dtSingleLine      = 0x20
	dtEndEllipsis     = 0x8000
	transparentBkMode = 1

	wdaExcludeFromCapture = 0x00000011

	smCxScreen = 0

	fwSemibold         = 600
	fwMedium           = 500
	fwRegular          = 400
	defaultCharset     = 1
	outDefaultPrecis   = 0
	clipDefaultPrecis  = 0
	antialiasedQuality = 4
	defaultPitch       = 0
	ffDontCare         = 0

	panelWidth   = 380
	panelHeight  = 104
	cornerRadius = 18
	screenMargin = 24

	sidePadding = 20
	dotRadius   = 5
	dotX        = sidePadding + dotRadius

	progressBarHeight = 4
	progressBarInset  = sidePadding
)

var (
	gdi32 = windows.NewLazySystemDLL("gdi32.dll")

	procRegisterClassExW           = user32.NewProc("RegisterClassExW")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procDefWindowProcW             = user32.NewProc("DefWindowProcW")
	procShowWindow                 = user32.NewProc("ShowWindow")
	procGetMessageW                = user32.NewProc("GetMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessageW           = user32.NewProc("DispatchMessageW")
	procPostQuitMessage            = user32.NewProc("PostQuitMessage")
	procPostMessageW               = user32.NewProc("PostMessageW")
	procBeginPaint                 = user32.NewProc("BeginPaint")
	procEndPaint                   = user32.NewProc("EndPaint")
	procGetClientRect              = user32.NewProc("GetClientRect")
	procFillRect                   = user32.NewProc("FillRect")
	procFrameRect                  = user32.NewProc("FrameRect")
	procDrawTextW                  = user32.NewProc("DrawTextW")
	procGetSystemMetrics           = user32.NewProc("GetSystemMetrics")
	procInvalidateRect             = user32.NewProc("InvalidateRect")
	procSetTextColor               = gdi32.NewProc("SetTextColor")
	procSetBkMode                  = gdi32.NewProc("SetBkMode")
	procSetTextCharacterExtra      = gdi32.NewProc("SetTextCharacterExtra")
	procCreateSolidBrush           = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject               = gdi32.NewProc("DeleteObject")
	procCreateRoundRectRgn         = gdi32.NewProc("CreateRoundRectRgn")
	procCreateFontW                = gdi32.NewProc("CreateFontW")
	procSelectObject               = gdi32.NewProc("SelectObject")
	procEllipse                    = gdi32.NewProc("Ellipse")
	procSetWindowRgn               = user32.NewProc("SetWindowRgn")
	procSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	procSetWindowDisplayAffinity   = user32.NewProc("SetWindowDisplayAffinity")
	procGetModuleHandleW           = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

type rect struct{ Left, Top, Right, Bottom int32 }

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       syscall.Handle
}

type paintStruct struct {
	hdc         syscall.Handle
	fErase      int32
	rcPaint     rect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
}

type point struct{ X, Y int32 }

type msgT struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

// Shared overlay state, protected by overlayMu: written from the update
// goroutine (any time a new OverlayState arrives), read from WM_PAINT on
// the UI thread.
var overlayMu sync.Mutex
var overlayStatus, overlayStepText string
var overlayStepIndex, overlayStepTotal int
var overlayVisible bool

var overlayBrush syscall.Handle
var labelFont, stepFont, counterFont syscall.Handle

func rgb(r, g, b byte) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

// Palette. A single accent color carries "in progress" across the status
// dot, the panel's top accent line, the progress fill, AND the full-screen
// HUD frame (hudframe.go reads statusColor() too), so the whole overlay
// reads as one system. Arc-reactor cyan for RUNNING gives it the
// Stark-HUD feel the flat indigo box didn't.
var (
	colorBg          = rgb(14, 16, 22)
	colorBorder      = rgb(40, 52, 66)
	colorAccent      = rgb(56, 189, 248) // arc-reactor cyan — RUNNING
	colorAmber       = rgb(255, 176, 32) // PAUSED
	colorGreen       = rgb(48, 209, 88)  // COMPLETED
	colorTextPrimary = rgb(238, 244, 250)
	colorTextMuted   = rgb(138, 154, 170)
	colorTrack       = rgb(32, 40, 50)
)

func createFont(height int32, weight uint32) syscall.Handle {
	face, _ := syscall.UTF16PtrFromString("Segoe UI")
	h, _, _ := procCreateFontW.Call(
		uintptr(height), 0, 0, 0, uintptr(weight),
		0, 0, 0,
		uintptr(defaultCharset), uintptr(outDefaultPrecis), uintptr(clipDefaultPrecis),
		uintptr(antialiasedQuality), uintptr(defaultPitch|ffDontCare),
		uintptr(unsafe.Pointer(face)),
	)
	return syscall.Handle(h)
}

// statusColor picks the dot/accent color for a given status.
func statusColor(status string) uint32 {
	switch status {
	case "PAUSED":
		return colorAmber
	case "COMPLETED":
		return colorGreen
	default: // RUNNING
		return colorAccent
	}
}

func drawText(hdc uintptr, s string, r rect, flags uintptr) {
	u16, _ := syscall.UTF16FromString(s)
	if len(u16) <= 1 {
		return // empty string — nothing to draw
	}
	procDrawTextW.Call(
		hdc,
		uintptr(unsafe.Pointer(&u16[0])),
		uintptr(len(u16)-1),
		uintptr(unsafe.Pointer(&r)),
		flags,
	)
}

func fillSolidRect(hdc uintptr, r rect, color uint32) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(color))
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), brush)
	procDeleteObject.Call(brush)
}

func wndProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmPaint:
		var ps paintStruct
		procBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		hdc := uintptr(ps.hdc)

		var cr rect
		procGetClientRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&cr)))

		overlayMu.Lock()
		status, stepText := overlayStatus, overlayStepText
		stepIndex, stepTotal := overlayStepIndex, overlayStepTotal
		overlayMu.Unlock()

		accent := statusColor(status)

		// Background.
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&cr)), uintptr(overlayBrush))

		// Accent line across the very top (2px), status-colored, tying the
		// panel to the full-screen frame. Hairline neutral border on the
		// other three sides.
		fillSolidRect(hdc, rect{cr.Left, cr.Top, cr.Right, cr.Top + 2}, accent)
		fillSolidRect(hdc, rect{cr.Left, cr.Bottom - 1, cr.Right, cr.Bottom}, colorBorder)
		fillSolidRect(hdc, rect{cr.Left, cr.Top, cr.Left + 1, cr.Bottom}, colorBorder)
		fillSolidRect(hdc, rect{cr.Right - 1, cr.Top, cr.Right, cr.Bottom}, colorBorder)

		// --- Row 1: status dot + uppercase label + right-aligned step counter ---
		const row1CenterY = 16 + sidePadding // 36
		dotBrush, _, _ := procCreateSolidBrush.Call(uintptr(accent))
		oldBrush, _, _ := procSelectObject.Call(hdc, dotBrush)
		procEllipse.Call(hdc,
			uintptr(dotX-dotRadius), uintptr(row1CenterY-dotRadius),
			uintptr(dotX+dotRadius), uintptr(row1CenterY+dotRadius),
		)
		procSelectObject.Call(hdc, oldBrush)
		procDeleteObject.Call(dotBrush)

		procSetBkMode.Call(hdc, uintptr(transparentBkMode))

		labelLeft := int32(dotX + dotRadius + 10)

		procSelectObject.Call(hdc, uintptr(labelFont))
		procSetTextColor.Call(hdc, uintptr(colorTextMuted))
		procSetTextCharacterExtra.Call(hdc, uintptr(1)) // slight letter-spacing for the small caps label
		labelRect := rect{Left: labelLeft, Top: row1CenterY - 10, Right: cr.Right - 100, Bottom: row1CenterY + 10}
		drawText(hdc, status, labelRect, dtLeft|dtVCenter|dtSingleLine)
		procSetTextCharacterExtra.Call(hdc, uintptr(0)) // reset — this HDC setting persists otherwise

		if stepTotal > 0 {
			counterText := itoa(stepIndex) + " / " + itoa(stepTotal)
			procSelectObject.Call(hdc, uintptr(counterFont))
			procSetTextColor.Call(hdc, uintptr(colorTextMuted))
			counterRect := rect{Left: cr.Right - 100, Top: row1CenterY - 10, Right: cr.Right - sidePadding, Bottom: row1CenterY + 10}
			drawText(hdc, counterText, counterRect, dtRight|dtVCenter|dtSingleLine)
		}

		// --- Row 2: current step text, larger and brighter ---
		procSelectObject.Call(hdc, uintptr(stepFont))
		procSetTextColor.Call(hdc, uintptr(colorTextPrimary))
		stepRect := rect{Left: sidePadding, Top: 44, Right: cr.Right - sidePadding, Bottom: 76}
		drawText(hdc, stepText, stepRect, dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis)

		// --- Row 3: thin progress bar ---
		barTop := int32(panelHeight - 20)
		barBottom := barTop + progressBarHeight
		trackRect := rect{Left: progressBarInset, Top: barTop, Right: cr.Right - progressBarInset, Bottom: barBottom}
		fillSolidRect(hdc, trackRect, colorTrack)

		if stepTotal > 0 {
			trackWidth := trackRect.Right - trackRect.Left
			fillWidth := trackWidth * int32(stepIndex) / int32(stepTotal)
			if fillWidth > 0 {
				fillRect := rect{Left: trackRect.Left, Top: barTop, Right: trackRect.Left + fillWidth, Bottom: barBottom}
				fillSolidRect(hdc, fillRect, accent)
			}
		}

		procEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		return 0

	case wmUpdate:
		overlayMu.Lock()
		visible := overlayVisible
		overlayMu.Unlock()
		if visible {
			procShowWindow.Call(uintptr(hwnd), uintptr(swShowNoActivate))
		} else {
			procShowWindow.Call(uintptr(hwnd), uintptr(swHide))
		}
		procInvalidateRect.Call(uintptr(hwnd), 0, 1)
		return 0

	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

// itoa avoids pulling in strconv just for this handful of small,
// always-non-negative numbers (step index/total).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// RunOverlay creates the window and pumps its message loop until done is
// closed. Must run on its own dedicated OS thread — Win32 windows are
// bound to the thread that created them.
func RunOverlay(updates <-chan OverlayState, done <-chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className, _ := syscall.UTF16PtrFromString("RemoteWorkerOverlayClass")
	windowName, _ := syscall.UTF16PtrFromString("RemoteWorker")

	hInstance, _, _ := procGetModuleHandleW.Call(0)

	wndProcPtr := syscall.NewCallback(wndProc)

	overlayBrush = createSolidBrushHandle(colorBg)
	labelFont = createFont(-12, fwSemibold)
	stepFont = createFont(-16, fwMedium)
	counterFont = createFont(-12, fwRegular)

	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   wndProcPtr,
		hInstance:     syscall.Handle(hInstance),
		hbrBackground: overlayBrush,
		lpszClassName: className,
	}
	ret, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if ret == 0 {
		log.Printf("overlay: RegisterClassExW failed: %v", err)
		return
	}

	// Anchor top-center: centered horizontally, just below the top edge,
	// so it sits inside the HUD frame like a readout rather than crammed
	// into a corner.
	screenWidth, _, _ := procGetSystemMetrics.Call(uintptr(smCxScreen))
	posX := (int32(screenWidth) - panelWidth) / 2
	if posX < 0 {
		posX = 0
	}
	posY := int32(screenMargin)

	hwndRaw, _, err := procCreateWindowExW.Call(
		uintptr(wsExLayered|wsExTransparent|wsExTopmost|wsExToolWindow|wsExNoActivate),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		uintptr(wsPopup),
		uintptr(posX), uintptr(posY),
		uintptr(panelWidth), uintptr(panelHeight),
		0, 0, hInstance, 0,
	)
	hwnd := syscall.Handle(hwndRaw)
	if hwnd == 0 {
		log.Printf("overlay: CreateWindowExW failed: %v", err)
		return
	}

	// Rounded corners.
	region, _, _ := procCreateRoundRectRgn.Call(0, 0, uintptr(panelWidth), uintptr(panelHeight), uintptr(cornerRadius), uintptr(cornerRadius))
	procSetWindowRgn.Call(uintptr(hwnd), region, 1)

	// Uniform translucency for the whole panel — a flat "smoked glass"
	// look. (See the doc comment above on why this doesn't use
	// DwmEnableBlurBehindWindow.)
	procSetLayeredWindowAttributes.Call(uintptr(hwnd), 0, uintptr(240), uintptr(lwaAlpha))

	// Never show up in screenshots this worker takes for the AI.
	procSetWindowDisplayAffinity.Call(uintptr(hwnd), uintptr(wdaExcludeFromCapture))

	// Full-screen glowing HUD frame, sharing this thread's message loop.
	// If it can't be created, we carry on with just the status panel.
	hudFrame = newHUDFrame(hInstance)
	if hudFrame == nil {
		log.Println("overlay: HUD frame unavailable, running with status panel only")
	}

	go func() {
		for {
			select {
			case st, ok := <-updates:
				if !ok {
					return
				}
				overlayMu.Lock()
				overlayVisible = st.Visible
				overlayStatus = st.Status
				overlayStepText = st.StepText
				overlayStepIndex = st.StepIndex
				overlayStepTotal = st.StepTotal
				overlayMu.Unlock()
				procPostMessageW.Call(uintptr(hwnd), uintptr(wmUpdate), 0, 0)
				if hudFrame != nil {
					procPostMessageW.Call(uintptr(hudFrame.hwnd), uintptr(wmUpdate), 0, 0)
				}
			case <-done:
				procPostMessageW.Call(uintptr(hwnd), uintptr(wmDestroy), 0, 0)
				return
			}
		}
	}()

	var m msgT
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	if hudFrame != nil {
		hudFrame.destroy()
	}
	procDeleteObject.Call(uintptr(overlayBrush))
	procDeleteObject.Call(uintptr(labelFont))
	procDeleteObject.Call(uintptr(stepFont))
	procDeleteObject.Call(uintptr(counterFont))
}

func createSolidBrushHandle(color uint32) syscall.Handle {
	h, _, _ := procCreateSolidBrush.Call(uintptr(color))
	return syscall.Handle(h)
}
