//go:build windows

package main

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Struct layout below matches the pattern confirmed working in
// golang.org/issue/31685 (a real, resolved Go bug report about SendInput
// from Go) rather than one derived from scratch: keybdInput/mouseInput
// mirror the real Win32 KEYBDINPUT/MOUSEINPUT layouts, and the trailing
// `padding uint64` on the keyboard variant pads it out to the same total
// size as the (larger) mouse variant, matching how the real C `INPUT`
// union is sized by its largest member.
const (
	inputTypeMouse    uint32 = 0
	inputTypeKeyboard uint32 = 1

	mouseEventFLeftDown  uint32 = 0x0002
	mouseEventFLeftUp    uint32 = 0x0004
	mouseEventFRightDown uint32 = 0x0008
	mouseEventFRightUp   uint32 = 0x0010

	keyEventFKeyUp   uint32 = 0x0002
	keyEventFUnicode uint32 = 0x0004
)

type mouseInput struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	dwExtraInfo uint64
}

type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uint64
}

type mouseInputEvent struct {
	inputType uint32
	mi        mouseInput
}

type keybdInputEvent struct {
	inputType uint32
	ki        keybdInput
	padding   uint64
}

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procSendInput    = user32.NewProc("SendInput")
	procSetCursorPos = user32.NewProc("SetCursorPos")
)

func sendMouseEvent(flags uint32) {
	ev := mouseInputEvent{
		inputType: inputTypeMouse,
		mi:        mouseInput{dwFlags: flags},
	}
	procSendInput.Call(
		uintptr(1),
		uintptr(unsafe.Pointer(&ev)),
		unsafe.Sizeof(ev),
	)
}

func sendKeybdEvent(scan uint16, flags uint32) {
	ev := keybdInputEvent{
		inputType: inputTypeKeyboard,
		ki:        keybdInput{wScan: scan, dwFlags: flags},
	}
	procSendInput.Call(
		uintptr(1),
		uintptr(unsafe.Pointer(&ev)),
		unsafe.Sizeof(ev),
	)
}

// MoveMouse moves the cursor to absolute screen coordinates.
func MoveMouse(x, y int) {
	procSetCursorPos.Call(uintptr(int32(x)), uintptr(int32(y)))
}

func LeftDown()  { sendMouseEvent(mouseEventFLeftDown) }
func LeftUp()    { sendMouseEvent(mouseEventFLeftUp) }
func RightDown() { sendMouseEvent(mouseEventFRightDown) }
func RightUp()   { sendMouseEvent(mouseEventFRightUp) }

// LeftClick performs a full press+release at the current cursor position.
func LeftClick() {
	LeftDown()
	time.Sleep(20 * time.Millisecond)
	LeftUp()
}

// RightClick performs a full press+release at the current cursor position.
func RightClick() {
	RightDown()
	time.Sleep(20 * time.Millisecond)
	RightUp()
}

// TypeText simulates keystrokes for arbitrary text using Unicode packet
// input (KEYEVENTF_UNICODE), which sidesteps virtual-key/layout mapping
// entirely. Covers the Basic Multilingual Plane; runes outside it (rare
// emoji, some CJK extensions) would need surrogate pairs, not implemented.
func TypeText(s string) {
	for _, r := range s {
		if r > 0xFFFF {
			continue
		}
		code := uint16(r)
		sendKeybdEvent(code, keyEventFUnicode)
		sendKeybdEvent(code, keyEventFUnicode|keyEventFKeyUp)
		time.Sleep(8 * time.Millisecond)
	}
}
