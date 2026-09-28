//go:build windows

package main

import (
	"image"
	"log"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The overlay shows, on the controlled computer, that the machine is
// driving and what it's doing:
//   - a soft glow around the edge of the screen — a gradient that slowly
//     flows around the screen while a task runs, amber and breathing when
//     paused or waiting for you on your phone, green-teal when done;
//   - a floating status pill at the top centre: status, step "3 / 7", the
//     current step, the live action, and a progress bar. It fades almost
//     away while the cursor is under it, so it never hides what's being
//     clicked.
//
// What it looks like is drawn in plain Go (overlay_render.go); this file
// holds the shared Win32 declarations and RunOverlay, which owns the
// windows (layered.go) and their message loop, timer and fades.
//
// Every window excludes itself from screen capture (SetWindowDisplayAffinity /
// WDA_EXCLUDEFROMCAPTURE), so none of it appears in the screenshots sent to
// the AI. REMOTE_WORKER_OVERLAY_STATIC=1 turns the animation off.
//
// NOTE: this is standard but unverified-on-hardware Win32 — build and
// eyeball before a demo. REMOTE_WORKER_NO_OVERLAY=1 disables it entirely.

const (
	wsPopup         = 0x80000000
	wsExLayered     = 0x00080000
	wsExTransparent = 0x00000020
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExNoActivate  = 0x08000000

	swHide           = 0
	swShowNoActivate = 4

	wmDestroy = 0x0002
	wmTimer   = 0x0113
	wmApp     = 0x8000
	wmUpdate  = wmApp + 1 // custom message: "re-read shared state and redraw"

	wdaExcludeFromCapture = 0x00000011

	smCxScreen = 0
)

var (
	gdi32 = windows.NewLazySystemDLL("gdi32.dll")

	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procShowWindow               = user32.NewProc("ShowWindow")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procDeleteObject             = gdi32.NewProc("DeleteObject")
	procSelectObject             = gdi32.NewProc("SelectObject")
	procSetWindowDisplayAffinity = user32.NewProc("SetWindowDisplayAffinity")
	procGetModuleHandleW         = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procSetTimer                 = user32.NewProc("SetTimer")
	procKillTimer                = user32.NewProc("KillTimer")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
)

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

type point struct{ X, Y int32 }

type msgT struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

// Shared overlay state, protected by overlayMu: written by the update
// goroutine, read on the UI thread.
var (
	overlayMu      sync.Mutex
	overlayPending OverlayState
)

const (
	frameTimerID  = 1
	frameInterval = 33 // ms — the pill animates at ~30fps
	edgeEvery     = 2  // the edge glow redraws every 2nd frame (~15fps): it moves slowly
	fadeStep      = 40 // opacity change per frame when fading in/out
)

// overlayUI is the overlay's state on the UI thread (only touched there).
type overlayUI struct {
	edges     []*layeredWindow
	masks     []*edgeMask
	pill      *layeredWindow
	pillBase  *image.RGBA
	pillBody  image.Rectangle
	faces     pillFaces
	scale     float64
	state     OverlayState
	start     time.Time
	frame     int
	opacity   int // overall 0–255, for fading in/out
	pillAlpha int // extra fade while the cursor is under the pill
	timerOn   bool
	static    bool // REMOTE_WORKER_OVERLAY_STATIC=1
	dirty     bool
}

var ui *overlayUI

func overlayWndProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmUpdate:
		if ui != nil {
			overlayMu.Lock()
			st := overlayPending
			overlayMu.Unlock()
			ui.apply(st)
		}
		return 0
	case wmTimer:
		if ui != nil {
			ui.tick()
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

// systemScale is the display scaling factor (1.0 at 100%). The process is
// DPI-aware (dpi.go), so everything is drawn in real pixels.
func systemScale() float64 {
	if procGetDpiForSystem.Find() == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi > 0 {
			return float64(dpi) / 96
		}
	}
	return 1
}

// apply takes a new state from the executor or the takeover monitor.
func (u *overlayUI) apply(st OverlayState) {
	// The takeover monitor only knows the status: keep the step details.
	if st.Visible && st.StepText == "" && st.TaskDescription == "" && u.state.Visible {
		prev := u.state
		prev.Status = st.Status
		st = prev
	}
	if st.Visible && !u.state.Visible {
		u.start = time.Now()
	}
	u.state = st
	u.dirty = true
	u.ensureTimer()
	u.tick()
}

// ensureTimer runs the frame timer while anything is on screen or fading.
func (u *overlayUI) ensureTimer() {
	if u.timerOn {
		return
	}
	procSetTimer.Call(uintptr(u.pill.hwnd), frameTimerID, frameInterval, 0)
	u.timerOn = true
}

func (u *overlayUI) stopTimer() {
	if u.timerOn {
		procKillTimer.Call(uintptr(u.pill.hwnd), frameTimerID)
		u.timerOn = false
	}
}

// tick advances fades and animation by one frame and pushes what changed.
func (u *overlayUI) tick() {
	u.frame++
	secs := time.Since(u.start).Seconds()
	th := themeFor(u.state.Status)

	// Fade the whole overlay in or out.
	target := 0
	if u.state.Visible {
		target = 255
	}
	u.opacity = approach(u.opacity, target, fadeStep)
	if u.opacity == 0 {
		for _, e := range u.edges {
			e.hide()
		}
		u.pill.hide()
		u.stopTimer()
		return
	}

	// Fade the pill away while the cursor is under it.
	var cur point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
	pillTarget := 255
	if u.pill.contains(cur, u.pillBody) {
		pillTarget = pillFadedTo
	}
	u.pillAlpha = approach(u.pillAlpha, pillTarget, fadeStep)

	animate := th.animated() && !u.static
	if u.dirty || (animate && u.frame%edgeEvery == 0) {
		phase := secs * 0.04 // one lap around the screen every 25s
		for i, e := range u.edges {
			renderEdge(e.img, u.masks[i], th, phase, th.intensity(secs))
			e.push(byte(u.opacity))
		}
	} else {
		for _, e := range u.edges {
			e.setOpacity(byte(u.opacity))
		}
	}

	pillOpacity := byte(u.opacity * u.pillAlpha / 255)
	if u.dirty || animate {
		renderPill(u.pill.img, u.pillBase, u.state, u.faces, u.scale, secs)
		u.pill.push(pillOpacity)
	} else {
		u.pill.setOpacity(pillOpacity)
	}
	u.dirty = false

	// Fully shown and nothing moving: the timer only has to keep the
	// cursor fade responsive, which costs next to nothing.
}

func approach(v, target, step int) int {
	switch {
	case v < target:
		return min(v+step, target)
	case v > target:
		return max(v-step, target)
	}
	return v
}

// RunOverlay creates the overlay windows and pumps their message loop until
// done is closed. Must run on its own dedicated OS thread — Win32 windows
// are bound to the thread that created them.
func RunOverlay(updates <-chan OverlayState, done <-chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	if !registerOverlayClass(hInstance, syscall.NewCallback(overlayWndProc)) {
		log.Println("overlay: could not register the window class; overlay disabled")
		drain(updates)
		return
	}

	sw, _, _ := procGetSystemMetrics.Call(uintptr(smCxScreen))
	sh, _, _ := procGetSystemMetrics.Call(uintptr(smCyScreen))
	w, h := int(int32(sw)), int(int32(sh))
	scale := systemScale()

	u := &overlayUI{
		scale:     scale,
		faces:     newPillFaces(scale),
		pillBase:  newPillBase(scale),
		pillBody:  pillBodyRect(scale),
		static:    os.Getenv("REMOTE_WORKER_OVERLAY_STATIC") == "1",
		pillAlpha: 255,
		start:     time.Now(),
	}
	for _, r := range edgeStrips(w, h, scale) {
		lw := newLayeredWindow(hInstance, r)
		if lw == nil {
			log.Println("overlay: could not create an edge window; overlay disabled")
			drain(updates)
			return
		}
		u.edges = append(u.edges, lw)
		u.masks = append(u.masks, newEdgeMask(r, w, h, scale))
	}
	pw, ph := pillSize(scale)
	at := pillOrigin(w, scale)
	u.pill = newLayeredWindow(hInstance, image.Rect(at.X, at.Y, at.X+pw, at.Y+ph))
	if u.pill == nil {
		log.Println("overlay: could not create the status pill; overlay disabled")
		drain(updates)
		return
	}
	ui = u
	log.Printf("overlay: ready (%dx%d screen, %.0f%% scaling)", w, h, scale*100)

	go func() {
		for {
			select {
			case st, ok := <-updates:
				if !ok {
					return
				}
				overlayMu.Lock()
				overlayPending = st
				overlayMu.Unlock()
				procPostMessageW.Call(uintptr(u.pill.hwnd), uintptr(wmUpdate), 0, 0)
			case <-done:
				procPostMessageW.Call(uintptr(u.pill.hwnd), uintptr(wmDestroy), 0, 0)
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

	u.stopTimer()
	for _, e := range u.edges {
		e.destroy()
	}
	u.pill.destroy()
}

// drain keeps reading updates when the overlay couldn't start, so the
// executor's sends never block (see main.go).
func drain(updates <-chan OverlayState) {
	go func() {
		for range updates {
		}
	}()
}
