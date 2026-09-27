//go:build windows

package main

import (
	"strconv"
	"strings"
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

	// Virtual-key codes for the control keys the model can request inside
	// key_string. These CANNOT be sent as Unicode packets: a Unicode "\n"
	// packet does not press Enter on Windows (Enter is the VK_RETURN virtual
	// key, not the line-feed character), so typing "\n" via KEYEVENTF_UNICODE
	// does nothing useful — which is why "search and press Enter" flows
	// silently failed. These go through the virtual-key path instead.
	vkBack   uint16 = 0x08
	vkTab    uint16 = 0x09
	vkReturn uint16 = 0x0D
	vkEscape uint16 = 0x1B
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
	takeover.markWorkerInput() // this is the worker acting, not the human
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

// sendVKDown / sendVKUp press or release a single virtual key (VK_*).
// Splitting down and up (rather than only a combined press) is what makes
// modifier chords possible: hold Ctrl/Alt/Shift/Win DOWN, tap the main
// key, then release the modifiers in reverse — e.g. Win+D, Alt+F4,
// Ctrl+Shift+Esc.
func sendVKDown(vk uint16) {
	ev := keybdInputEvent{inputType: inputTypeKeyboard, ki: keybdInput{wVk: vk}}
	procSendInput.Call(uintptr(1), uintptr(unsafe.Pointer(&ev)), unsafe.Sizeof(ev))
}

func sendVKUp(vk uint16) {
	ev := keybdInputEvent{inputType: inputTypeKeyboard, ki: keybdInput{wVk: vk, dwFlags: keyEventFKeyUp}}
	procSendInput.Call(uintptr(1), uintptr(unsafe.Pointer(&ev)), unsafe.Sizeof(ev))
}

// sendVirtualKey presses and releases a single virtual key. Used for keys
// with no meaningful Unicode-packet form — Enter, Tab, Backspace, Esc.
func sendVirtualKey(vk uint16) {
	sendVKDown(vk)
	time.Sleep(8 * time.Millisecond)
	sendVKUp(vk)
	time.Sleep(8 * time.Millisecond)
}

// keyNameToVK maps the special-key token names the planner may put inside
// {curly braces} in key_string to their Windows virtual-key codes. This is
// what lets the model press keys that aren't literal characters — the
// Windows key above all, but also navigation and editing keys.
var keyNameToVK = map[string]uint16{
	"WIN": 0x5B, "LWIN": 0x5B, "WINDOWS": 0x5B, "RWIN": 0x5C,
	"CTRL": 0x11, "CONTROL": 0x11, "ALT": 0x12, "SHIFT": 0x10,
	"ENTER": 0x0D, "RETURN": 0x0D, "TAB": 0x09, "ESC": 0x1B, "ESCAPE": 0x1B,
	"BACKSPACE": 0x08, "BKSP": 0x08, "DELETE": 0x2E, "DEL": 0x2E,
	"INSERT": 0x2D, "INS": 0x2D, "SPACE": 0x20,
	"HOME": 0x24, "END": 0x23, "PAGEUP": 0x21, "PGUP": 0x21,
	"PAGEDOWN": 0x22, "PGDN": 0x22,
	"UP": 0x26, "DOWN": 0x28, "LEFT": 0x25, "RIGHT": 0x27,
	"CAPSLOCK": 0x14, "PRINTSCREEN": 0x2C, "PRTSC": 0x2C,
}

// resolveKeyName turns one token component ("WIN", "D", "F4", "ENTER") into
// a virtual-key code. Single letters/digits map to their VK directly (VK
// codes for A–Z and 0–9 equal their uppercase ASCII values); F1–F12 are
// handled numerically.
func resolveKeyName(p string) (uint16, bool) {
	u := strings.ToUpper(strings.TrimSpace(p))
	if u == "" {
		return 0, false
	}
	if vk, ok := keyNameToVK[u]; ok {
		return vk, true
	}
	if len(u) >= 2 && u[0] == 'F' {
		if n, err := strconv.Atoi(u[1:]); err == nil && n >= 1 && n <= 12 {
			return uint16(0x70 + n - 1), true
		}
	}
	if len(u) == 1 {
		c := u[0]
		if c >= 'A' && c <= 'Z' {
			return uint16(c), true
		}
		if c >= '0' && c <= '9' {
			return uint16(c), true
		}
	}
	return 0, false
}

// handleKeyToken performs a special-key token like "WIN", "ENTER", or a
// chord like "WIN+D" / "CTRL+SHIFT+ESC". Returns false if any component is
// unrecognized, so the caller can fall back to typing the raw text.
func handleKeyToken(token string) bool {
	parts := strings.Split(token, "+")
	vks := make([]uint16, 0, len(parts))
	for _, p := range parts {
		vk, ok := resolveKeyName(p)
		if !ok {
			return false
		}
		vks = append(vks, vk)
	}
	if len(vks) == 0 {
		return false
	}
	// All but the last component are modifiers held down around the main key.
	mods := vks[:len(vks)-1]
	main := vks[len(vks)-1]
	for _, m := range mods {
		sendVKDown(m)
	}
	sendVKDown(main)
	time.Sleep(12 * time.Millisecond)
	sendVKUp(main)
	for i := len(mods) - 1; i >= 0; i-- {
		sendVKUp(mods[i])
	}
	time.Sleep(12 * time.Millisecond)
	return true
}

// MoveMouse moves the cursor to absolute screen coordinates.
func MoveMouse(x, y int) {
	takeover.markWorkerInput() // this move is the worker, not the human
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

// TypeText turns one KEYBOARD_INPUT's key_string into keystrokes. It has
// two modes, mixed freely within a single string:
//
//   - Literal characters are typed as Unicode packets (KEYEVENTF_UNICODE),
//     which sidesteps virtual-key/layout mapping and covers the Basic
//     Multilingual Plane (runes beyond it — rare emoji, some CJK
//     extensions — would need surrogate pairs, not implemented).
//   - {TOKEN} sequences press real keys and chords: {WIN}, {ENTER}, {TAB},
//     {ESC}, {WIN+D} (show desktop), {ALT+F4} (close window),
//     {CTRL+SHIFT+ESC}, arrows, F-keys, etc. See handleKeyToken.
//
// So "chrome{ENTER}" types "chrome" then presses Enter, and "{WIN}" alone
// opens the Start menu. The legacy control characters \n, \t, \b still map
// to Enter/Tab/Backspace so older outputs keep working.
func TypeText(s string) {
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		r := runes[i]

		if r == '{' {
			// Look for a matching '}' and try to handle the token inside.
			if j := indexRune(runes, '}', i+1); j >= 0 {
				if handleKeyToken(string(runes[i+1 : j])) {
					i = j + 1
					continue
				}
			}
			// Unrecognized — fall through and type '{' as a literal char.
		}

		switch r {
		case '\n', '\r':
			sendVirtualKey(vkReturn)
		case '\t':
			sendVirtualKey(vkTab)
		case '\b':
			sendVirtualKey(vkBack)
		default:
			if r <= 0xFFFF {
				code := uint16(r)
				sendKeybdEvent(code, keyEventFUnicode)
				sendKeybdEvent(code, keyEventFUnicode|keyEventFKeyUp)
				time.Sleep(8 * time.Millisecond)
			}
		}
		i++
	}
}

func indexRune(rs []rune, target rune, from int) int {
	for k := from; k < len(rs); k++ {
		if rs[k] == target {
			return k
		}
	}
	return -1
}
