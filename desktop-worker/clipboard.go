//go:build windows

package main

import (
	"errors"
	"runtime"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Clipboard access: reading what a copy put there (so the executor can
// check the copy worked — see clipboardAfterCopy in executor.go), and, with
// TYPE_WITH_PASTE=1, pasting long text instead of typing it (the user's
// clipboard is saved first and put back afterwards).

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

var (
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
)

// openClipboard retries briefly: another app can hold the clipboard open
// for a moment (clipboard managers, RDP), and OpenClipboard fails fast.
func openClipboard() error {
	for i := 0; i < 10; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("OpenClipboard failed (clipboard busy)")
}

// readClipboardText returns the clipboard's text and whether there was
// any. Non-text contents (an image, files) report ok=false.
func readClipboardText() (text string, ok bool, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); r == 0 {
		return "", false, nil
	}
	if err := openClipboard(); err != nil {
		return "", false, err
	}
	defer procCloseClipboard.Call()

	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false, nil
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", false, errors.New("GlobalLock failed")
	}
	defer procGlobalUnlock.Call(h)
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(p))), true, nil
}

// writeClipboardText replaces the clipboard's contents with text.
func writeClipboardText(text string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	data := append(utf16.Encode([]rune(text)), 0)
	size := uintptr(len(data) * 2)

	h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return errors.New("GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("GlobalLock failed")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(data)), data)
	procGlobalUnlock.Call(h)

	if err := openClipboard(); err != nil {
		procGlobalFree.Call(h)
		return err
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		// Ownership only passes to the system on success.
		procGlobalFree.Call(h)
		return errors.New("SetClipboardData failed")
	}
	return nil
}

// pasteText puts text on the clipboard, presses Ctrl+V, then restores what
// was on the clipboard before (text only — a non-text clipboard, e.g. an
// image, can't be saved this way and is left replaced by the typed text).
// Returns an error if the clipboard couldn't be used, so the caller can
// fall back to typing.
func pasteText(text string) error {
	prev, hadText, _ := readClipboardText()

	if err := writeClipboardText(text); err != nil {
		return err
	}
	handleKeyToken("CTRL+V")

	// The target app reads the clipboard while handling the paste; give it
	// time (a busy browser can take a while) before swapping the old
	// contents back in, or it pastes those instead.
	time.Sleep(600 * time.Millisecond)
	if hadText {
		_ = writeClipboardText(prev)
	}
	return nil
}
