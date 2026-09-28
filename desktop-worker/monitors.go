//go:build windows

package main

import (
	"log"
	"unsafe"
)

// Multi-monitor handling. Screen capture (captureImage) and all click
// coordinates cover the PRIMARY monitor only, so a task that starts with
// the active window on a second screen would have the AI looking at the
// wrong display. At the start of each task the foreground window is moved
// onto the primary monitor and maximized there.

const (
	smCMonitors             = 80
	monitorDefaultToNearest = 2
	monitorDefaultToPrimary = 1
	swpNoZOrder             = 0x0004
	swpNoSize               = 0x0001
	swMaximize              = 3
)

var (
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
	procMonitorFromPoint    = user32.NewProc("MonitorFromPoint")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procIsZoomed            = user32.NewProc("IsZoomed")
)

// ensureForegroundOnPrimary moves the foreground window onto the primary
// monitor (maximized) if it's on another one, and warns when more than
// one monitor is attached. Best effort: failures are logged, never fatal.
func ensureForegroundOnPrimary() {
	count, _, _ := procGetSystemMetrics.Call(smCMonitors)
	if count <= 1 {
		return
	}
	log.Printf("monitors: %d monitors attached — the AI only sees and clicks the PRIMARY monitor", count)

	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return
	}
	winMon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	// MonitorFromPoint takes a POINT by value; (0,0) is always on the
	// primary monitor. On amd64 the 8-byte struct is passed in one register.
	pt := point{0, 0}
	primary, _, _ := procMonitorFromPoint.Call(*(*uintptr)(unsafe.Pointer(&pt)), monitorDefaultToPrimary)
	if winMon == 0 || winMon == primary {
		return
	}

	log.Println("monitors: the foreground window is on a secondary monitor — moving it to the primary one")
	// A maximized window must be restored before it can move monitors.
	if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
		procShowWindow.Call(hwnd, swShowNormal)
	}
	procSetWindowPos.Call(hwnd, 0, 40, 40, 0, 0, swpNoZOrder|swpNoSize)
	procShowWindow.Call(hwnd, swMaximize)
}
