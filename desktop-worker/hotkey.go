//go:build windows

package main

import (
	"log"
	"runtime"
	"unsafe"
)

// Global kill switch: Ctrl+Alt+Shift+X on the laptop cancels the current
// task, even when the phone is offline. Pressing it stops the executor
// locally straight away (see localCancel in executor.go) and sends
// CANCEL_TASK so the server, the web app and the overlay all follow.

const (
	modAlt      = 0x0001
	modControl  = 0x0002
	modShift    = 0x0004
	modNoRepeat = 0x4000
	wmHotkey    = 0x0312
	vkX         = 0x58

	cancelHotkeyID = 1
)

var (
	procRegisterHotKey   = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey = user32.NewProc("UnregisterHotKey")
)

// RunCancelHotkey registers the hotkey and calls onPress every time it is
// pressed. RegisterHotKey delivers WM_HOTKEY to the registering thread's
// message queue, so this runs its own message loop on a locked OS thread,
// for the life of the process.
func RunCancelHotkey(onPress func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	r, _, err := procRegisterHotKey.Call(0, cancelHotkeyID, modControl|modAlt|modShift|modNoRepeat, vkX)
	if r == 0 {
		log.Printf("hotkey: could not register Ctrl+Alt+Shift+X (another app may own it): %v — cancel from the phone instead", err)
		return
	}
	defer procUnregisterHotKey.Call(0, cancelHotkeyID)
	log.Println("hotkey: Ctrl+Alt+Shift+X cancels the current task")

	var m msgT
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		if m.Message == wmHotkey && m.WParam == cancelHotkeyID {
			log.Println("hotkey: Ctrl+Alt+Shift+X pressed — cancelling the current task")
			onPress()
		}
	}
}
