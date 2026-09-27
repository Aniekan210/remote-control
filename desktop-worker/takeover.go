//go:build windows

package main

import (
	"log"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Human-takeover pause: when the person physically moves the mouse, the
// worker pauses its automation so it doesn't fight the user for control.
//
// Behavior (as requested):
//   - A real (human) mouse movement pauses automation immediately.
//   - After the movement ENDS (the mouse goes still), the worker waits
//     takeoverResumeDelay (3s). If nothing else happens, it auto-resumes.
//   - If the human moves AGAIN during that 3-second window, the worker stops
//     auto-resuming and HOLDS until an explicit resume arrives from the
//     server over the WebSocket (the person acting through the web app) —
//     see isUserResumeSignal in wsclient.go for what counts.
//
// Distinguishing the human's movement from the worker's own: a low-level
// mouse hook (WH_MOUSE_LL) sees every mouse event with an "injected" flag
// that is set for programmatic input (SendInput). Real hardware movement is
// NOT injected. As a second guard, the worker stamps a short window around
// its own mouse calls (markWorkerInput) and ignores movement inside it, in
// case SetCursorPos-generated moves aren't flagged as injected on some
// Windows builds.
//
// NOTE: unverified on real hardware. If it misbehaves, set
// REMOTE_WORKER_NO_TAKEOVER=1 to disable it entirely.

const (
	whMouseLL     = 14
	wmMouseMove   = 0x0200
	llmhfInjected = 0x00000001

	takeoverReleaseDebounce = 250 * time.Millisecond // stillness that marks "released"
	takeoverResumeDelay     = 3 * time.Second        // wait after release before auto-resume
	takeoverWorkerWindow    = 160 * time.Millisecond // ignore movement this long after our own input
)

var (
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
)

type msllHookStruct struct {
	pt          point
	mouseData   uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

type takeoverState int

const (
	tActive    takeoverState = iota // automation may proceed
	tMoving                         // human gesture in progress
	tCountdown                      // gesture ended, counting down to auto-resume
	tHeld                           // second gesture — hold until server resume
)

// Takeover is the shared human-takeover monitor (one per process). When the
// human takes over, it PAUSES THE RUNNING TASK (sends PAUSE_TASK so the whole
// system — server, web app, overlay — reflects the pause), and resumes it
// (RESUME_TASK) either automatically after the 3s window or when the person
// resumes from the app.
type Takeover struct {
	mu          sync.Mutex
	cond        *sync.Cond
	state       takeoverState
	lastMove    time.Time
	releasedAt  time.Time
	workerUntil time.Time

	emit          chan string         // "pause"/"resume" → runSender (off the mutex)
	overlay       chan<- OverlayState // flip the border color immediately
	pauseFn       func()              // sends PAUSE_TASK to the server
	resumeFn      func()              // sends RESUME_TASK to the server
	isTaskRunning func() bool         // only engage while a task is actually running
}

var takeover *Takeover

// NewTakeover wires the monitor to the ways it affects the rest of the
// system: pauseFn/resumeFn send PAUSE_TASK/RESUME_TASK over the WebSocket,
// isTaskRunning reports whether a task is currently RUNNING (so idle mouse
// movement doesn't pause a non-existent task), and overlay lets it recolor
// the border the instant it pauses/resumes.
func NewTakeover(pauseFn, resumeFn func(), isTaskRunning func() bool, overlay chan<- OverlayState) *Takeover {
	t := &Takeover{
		state:         tActive,
		emit:          make(chan string, 8),
		overlay:       overlay,
		pauseFn:       pauseFn,
		resumeFn:      resumeFn,
		isTaskRunning: isTaskRunning,
	}
	t.cond = sync.NewCond(&t.mu)
	return t
}

// emitLocked / pushOverlayLocked are non-blocking sends, safe to call while
// holding t.mu (they never block the hook callback or the ticker).
func (t *Takeover) emitLocked(kind string) {
	select {
	case t.emit <- kind:
	default:
	}
}

func (t *Takeover) pushOverlayLocked(status string) {
	if t.overlay == nil {
		return
	}
	select {
	case t.overlay <- OverlayState{Visible: true, Status: status}:
	default:
	}
}

// runSender performs the network sends (PAUSE_TASK/RESUME_TASK) off the
// mutex, so a slow send never stalls the mouse hook.
func (t *Takeover) runSender() {
	for kind := range t.emit {
		switch kind {
		case "pause":
			if t.pauseFn != nil {
				t.pauseFn()
			}
		case "resume":
			if t.resumeFn != nil {
				t.resumeFn()
			}
		}
	}
}

// markWorkerInput records that the worker itself just moved or clicked, so
// the resulting events aren't mistaken for the human taking over. Safe on a
// nil receiver (takeover disabled).
func (t *Takeover) markWorkerInput() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.workerUntil = time.Now().Add(takeoverWorkerWindow)
	t.mu.Unlock()
}

// humanMove is called from the hook for each NON-injected mouse move.
func (t *Takeover) humanMove() {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if now.Before(t.workerUntil) {
		return // our own movement — ignore
	}
	switch t.state {
	case tActive:
		// Only engage if a task is actually running — otherwise there's
		// nothing to pause and we'd wrongly flip an idle task to PAUSED.
		if t.isTaskRunning != nil && !t.isTaskRunning() {
			return
		}
		t.state = tMoving
		t.emitLocked("pause")         // PAUSE_TASK → the running task goes PAUSED
		t.pushOverlayLocked("PAUSED") // border turns amber immediately
		log.Println("takeover: human mouse movement detected — pausing the running task")
	case tCountdown:
		t.state = tHeld
		log.Println("takeover: more movement during cooldown — holding until you resume from the app")
	}
	t.lastMove = now
}

// tick advances the time-based transitions; called periodically.
func (t *Takeover) tick() {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.state {
	case tMoving:
		if now.Sub(t.lastMove) >= takeoverReleaseDebounce {
			t.state = tCountdown
			t.releasedAt = now
		}
	case tCountdown:
		if now.Sub(t.releasedAt) >= takeoverResumeDelay {
			t.state = tActive
			t.emitLocked("resume")         // RESUME_TASK → the task goes RUNNING again
			t.pushOverlayLocked("RUNNING") // border back to cyan
			log.Println("takeover: no further movement — resuming the task")
			t.cond.Broadcast()
		}
	}
}

// Gate blocks until automation is allowed to proceed. Safe on nil.
func (t *Takeover) Gate() {
	if t == nil {
		return
	}
	t.mu.Lock()
	for t.state != tActive {
		t.cond.Wait()
	}
	t.mu.Unlock()
}

// ServerResume clears any pause because an explicit command arrived from the
// server (the person acting through the web app). Safe on nil.
func (t *Takeover) ServerResume() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.state != tActive {
		t.state = tActive
		// The app already sent RESUME_TASK (that's what triggered this), so
		// we do NOT emit another resume — just release the local gate and
		// recolor the border.
		t.pushOverlayLocked("RUNNING")
		log.Println("takeover: resume received from the app — resuming the task")
		t.cond.Broadcast()
	}
	t.mu.Unlock()
}

func takeoverHookProc(nCode uintptr, wParam uintptr, lParam uintptr) uintptr {
	if int32(nCode) >= 0 && wParam == wmMouseMove && takeover != nil {
		ms := (*msllHookStruct)(unsafe.Pointer(lParam))
		if ms.flags&llmhfInjected == 0 {
			takeover.humanMove()
		}
	}
	ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return ret
}

// RunTakeover installs the low-level mouse hook and pumps the message loop
// it requires. Runs on its own dedicated OS thread until done is closed
// (the ticker goroutine) / the process exits.
func RunTakeover(t *Takeover, done <-chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	hook, _, err := procSetWindowsHookExW.Call(uintptr(whMouseLL), syscall.NewCallback(takeoverHookProc), hInstance, 0)
	if hook == 0 {
		log.Printf("takeover: SetWindowsHookExW failed: %v — human-takeover pause disabled", err)
		return
	}
	log.Println("takeover: mouse hook installed — moving the mouse pauses the running task")
	defer procUnhookWindowsHookEx.Call(hook)

	// Network sends (PAUSE_TASK/RESUME_TASK) happen here, off the hook/mutex.
	go t.runSender()

	go func() {
		tk := time.NewTicker(50 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				t.tick()
			}
		}
	}()

	// Message loop — required so the system can invoke the low-level hook on
	// this thread. GetMessageW blocks; the hook fires during the wait.
	var m msgT
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
