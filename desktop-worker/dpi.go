//go:build windows

package main

import "log"

// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2, per Microsoft's
// DPI_AWARENESS_CONTEXT values (winuser.h). These are sentinel values, not
// real handles — defined as small negative numbers cast to a
// pointer-sized type. -4 is PER_MONITOR_AWARE_V2, the modern one
// (Windows 10 1703+).
var dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3) // -4, wrapped to uintptr's width

// setDPIAware MUST run before any window or device context is created —
// both screen capture (screenshot.go) and the overlay window (overlay.go)
// depend on it. Without declaring the process DPI-aware, Windows applies
// automatic DPI virtualization to legacy GDI/USER32 calls: GetDC, BitBlt
// and GetDIBits end up operating against a scaled-down "virtual" desktop,
// while other APIs report the true physical resolution — including
// EnumDisplaySettings, which the screenshot library deliberately uses to
// compute display bounds at the REAL resolution rather than the scaled
// one. That mismatch — a bitmap allocated at real resolution vs. BitBlt
// populating it from the scaled/virtualized screen DC — is a
// well-documented cause of GetDIBits failing outright. Since 125%/150%
// scaling is the Windows default on most modern laptops, this fails
// deterministically on the very first capture rather than intermittently,
// which is exactly the symptom this fixes.
func setDPIAware() {
	if proc := user32.NewProc("SetProcessDpiAwarenessContext"); proc.Find() == nil {
		if ret, _, _ := proc.Call(dpiAwarenessContextPerMonitorAwareV2); ret != 0 {
			log.Println("dpi: process set to per-monitor-v2 DPI aware")
			return
		}
		log.Println("dpi: SetProcessDpiAwarenessContext call failed, falling back")
	}
	// Fallback for pre-1703 Windows: coarser system-DPI-only awareness,
	// but it still fixes the BitBlt/GetDIBits virtualization mismatch
	// above, just without true per-monitor correctness on multi-DPI setups.
	if proc := user32.NewProc("SetProcessDPIAware"); proc.Find() == nil {
		proc.Call()
		log.Println("dpi: process set to system DPI aware (fallback)")
		return
	}
	log.Println("dpi: no DPI-awareness API available on this Windows version — screenshot capture may still fail on scaled displays")
}
