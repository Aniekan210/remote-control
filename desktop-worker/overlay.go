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

// The overlay is a single element: a full-screen glowing neon border that
// frames the display while the AI is in control, its color tracking the
// task status (cyan while RUNNING, amber while PAUSED, green when DONE).
// There is no status panel — the border alone signals "the machine is
// driving," staying out of the way of the actual screen content.
//
// The border is drawn by HUDFrame (hudframe.go), a per-pixel-alpha layered
// window. This file holds the shared Win32 declarations, the color system,
// and RunOverlay, which owns the message loop that the frame lives on.
//
// The frame excludes itself from screen capture (SetWindowDisplayAffinity /
// WDA_EXCLUDEFROMCAPTURE), so it never appears in the screenshots sent to
// the AI.
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
// goroutine, read by the frame's render on the UI thread. The frame only
// needs the status (for color) and whether it should be visible.
var overlayMu sync.Mutex
var overlayStatus string
var overlayVisible bool

func rgb(r, g, b byte) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

// Palette — strong neon, one color per task state. statusColor() is the
// single source of truth for the border color, so RUNNING/PAUSED/COMPLETED
// each read as an unmistakable glow.
var (
	colorAccent = rgb(40, 200, 255) // neon cyan — RUNNING
	colorAmber  = rgb(255, 176, 40) // neon amber — PAUSED
	colorGreen  = rgb(45, 220, 110) // neon green — COMPLETED
)

// statusColor picks the border glow color for a given status.
func statusColor(status string) uint32 {
	switch status {
	case "PAUSED", "NEEDS_INPUT":
		return colorAmber
	case "COMPLETED":
		return colorGreen
	default: // RUNNING
		return colorAccent
	}
}

// RunOverlay creates the neon border window and pumps its message loop
// until done is closed. Must run on its own dedicated OS thread — Win32
// windows are bound to the thread that created them.
func RunOverlay(updates <-chan OverlayState, done <-chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInstance, _, _ := procGetModuleHandleW.Call(0)

	hudFrame = newHUDFrame(hInstance)
	if hudFrame == nil {
		log.Println("overlay: HUD frame could not be created; overlay disabled")
		return
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
				overlayMu.Unlock()
				procPostMessageW.Call(uintptr(hudFrame.hwnd), uintptr(wmUpdate), 0, 0)
			case <-done:
				procPostMessageW.Call(uintptr(hudFrame.hwnd), uintptr(wmDestroy), 0, 0)
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

	hudFrame.destroy()
}
