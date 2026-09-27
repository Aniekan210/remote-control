# RemoteWorker

Windows-only desktop worker for the Remote Control system. Matches the
Task/Execution/Action schema from your web server.

**This now compiles clean.** I cross-compiled it for `windows/amd64` from
here (`go build`, `go vet`, `gofmt` all pass with zero errors) before
handing it back, so the file-count/`undefined` errors from before are
resolved. I still can't render a Win32 window from this environment, so
the overlay's on-screen appearance is the one thing left to eyeball on
real Windows — see "Known risks" below.

## Build

**Use `go build .` (or `go run .`), not `go build main.go` / `go run main.go`.**
Go only compiles the files you name explicitly — `main.go` alone doesn't
pull in `executor.go`, `wsclient.go`, etc., which is exactly what produced
the wall of `undefined: X` errors. The `.` tells it "build the whole
package in this directory."

```
go mod tidy
go build -ldflags "-H=windowsgui" -o remoteworker.exe .
```

`-H=windowsgui` suppresses the console window so it runs quietly in the
background. **Drop that flag while you're debugging** — you want the
console (and its log output) visible until you've confirmed the overlay
and input code work.

If `go mod tidy` can't reach `golang.org` in your environment (some
sandboxes block it), `go.mod` already has a `replace` line pointing
`golang.org/x/sys` at its GitHub mirror, which fixed that exact problem
when I hit it here. Delete the `replace` line if your machine can reach
`golang.org` directly — either way works.

## Run

```
set REMOTE_SERVER_ADDR=localhost:8080
remoteworker.exe
```

On first launch, it:
1. Generates a random device ID and caches it at `%LOCALAPPDATA%\RemoteWorker\device.id`. This ID never changes/regenerates on later launches — it's read back from that file every time.
2. Calls `POST /rooms` on your server itself — no manual `curl` needed. Runs every boot (not just the first), since your server's room table is in-memory and doesn't survive a restart; a 409 (room already exists) is treated as success.
3. Writes a small local HTML page at `%LOCALAPPDATA%\RemoteWorker\pairing.html` showing the device ID as a QR code, and opens it in your default browser.

Step 3 runs **only** on that first-ever launch (when a device ID doesn't exist yet and one gets generated). Every launch after that reuses the cached ID silently and skips the popup. To see the pairing page again later, delete `%LOCALAPPDATA%\RemoteWorker\device.id` (or the whole `RemoteWorker` folder) and relaunch — that's exactly what a first run looks like again. `REMOTE_WORKER_NO_PAIRING_UI=1` suppresses just the browser auto-open on that first launch, if you want the HTML file written without a tab popping open.

The device ID is also printed in plain text to the console/log on every single launch regardless of any of the above — if you just need the raw string, it's right there without touching the QR flow at all.

## If it seems to do nothing at all

Every run (including `-H=windowsgui` builds) now writes to
`%LOCALAPPDATA%\RemoteWorker\worker.log`, in addition to the console when
one exists. **Check that file first** — it starts with a line proving the
process actually started, then a step-by-step trace of what happened.

If `worker.log` doesn't exist or wasn't updated after you ran the exe, the
process never reached Go code at all — this points outside the code, most
likely at **antivirus**. An unsigned Go binary that does `SendInput`,
screen capture, and creates a hidden click-through window matches
Windows Defender's heuristics for a RAT almost exactly, and Defender will
silently quarantine or delete the `.exe` on sight on a lot of default
Windows setups. Check:
1. **Windows Security → Protection history** for an entry around when you ran it.
2. Whether `remoteworker.exe` is still sitting in your folder after running it — if Defender took it, it's gone.

If that's what's happening: add a Defender exclusion for your project
folder while you develop, or temporarily disable real-time protection for
testing. You'll eventually want to code-sign the binary for anything
beyond a hackathon demo.

If `worker.log` shows the process starting and getting partway through,
but nothing after a certain point, that's the actual crash site — send me
the log's contents and I'll dig in from there.

If you suspect the overlay specifically (raw Win32 code I couldn't test on
real Windows — see below), isolate it:

```
set REMOTE_WORKER_NO_OVERLAY=1
remoteworker.exe
```

If the worker runs fine with the overlay disabled, that confirms the
overlay code as the problem, and you at least have a working worker (no
on-screen indicator) for your demo while that gets fixed separately.

## What's implemented

- **WebSocket client** (`wsclient.go`) — connects to `/ws?id=<device_id>`,
  reconnects with backoff on drop, resyncs automatically (the server
  re-sends current state on every new connection).
- **Self-registration** (`register.go`) — calls `POST /rooms` on boot with
  retries; treats 409 as success.
- **First-run pairing UI** (`pairing.go`) — QR code of the device ID,
  opened in the default browser via `ShellExecute`, only on the boot that
  generated a new device ID.
- **Executor** (`executor.go`) — the ADVANCE state machine: runs an
  `ExecutionList` when one exists, otherwise requests the next step
  (with the filesystem snapshot attached only on the very first ADVANCE
  of a task).
- **Input** (`input.go`) — mouse move via `SetCursorPos`, clicks/holds via
  `SendInput` with the exact `MOUSEINPUT`/`KEYBDINPUT` struct layout from
  a documented, confirmed-working Go/`SendInput` reference (not something
  I derived from scratch this time), typing via `SendInput` Unicode key
  packets (handles arbitrary text, BMP only).
- **Screenshot** (`screenshot.go`) — primary display only, PNG-encoded.
- **Filesystem watcher** (`fswatcher.go`) — Desktop/Documents/
  Downloads/Pictures by default, kept live with `fsnotify`. Widen
  `defaultWatchRoots()` if you want more coverage — walking/watching an
  entire drive is slower and can hit OS watch-handle limits, so it's not
  the default.
- **Overlay** (`overlay.go`) — translucent dark panel anchored to the
  actual top-right of the screen (reads real screen width via
  `GetSystemMetrics`, not a hardcoded coordinate), rounded corners, a thin
  border, a colored status dot (green = running, amber = paused), two
  lines of Segoe UI text (bold title + muted, ellipsized detail line),
  topmost, click-through, hidden from taskbar/alt-tab, and excluded from
  screen captures (`SetWindowDisplayAffinity`) so it never ends up in the
  screenshots sent to the AI.

## Verified vs. not yet verified

**Verified here:** the whole package builds cleanly for `windows/amd64`
(`go build`, `go vet`, `gofmt` all clean) with both the debug and
`-H=windowsgui` release builds. That means every symbol resolves, every
struct/syscall signature matches what its Go binding expects, and there
are no syntax or type errors anywhere in the codebase.

**Not verified (no Windows machine here to render a window on):**
1. **The overlay's actual on-screen appearance** — position, whether the
   rounded corners/border/translucency look right together, whether the
   status dot and text line up as intended. The Win32 calls are
   individually correct and documented, but I can't see the composed
   result. Build it and look at it early.
2. Overlay position is computed from `GetSystemMetrics(SM_CXSCREEN)` at
   startup — correct for a single-monitor primary-display setup; a
   multi-monitor arrangement where the primary isn't where you'd expect
   could put it somewhere unexpected.
3. `TypeText` only handles Unicode code points in the Basic Multilingual
   Plane — fine for normal text, but rare emoji/surrogate-pair characters
   are silently skipped.
4. Drag gestures assume a start-hold / move / end-hold sequence arrives as
   one `ExecutionList` (matches the planner's system prompt) — if the AI
   ever splits a drag across two separate `ADVANCE` round trips, the
   hold-state tracking (currently local to a single `executeList` call)
   would need to move into `State`.
5. No DPI-awareness manifest — on a scaled display, screenshot coordinates
   vs. `SetCursorPos` coordinates could disagree. Worth testing on
   whatever display setup you're demoing on.

## Nice-to-haves if you have time left

- I looked into `DwmEnableBlurBehindWindow` for a true frosted-glass
  effect and deliberately skipped it: Microsoft's own docs state it
  stopped producing an actual blur starting with Windows 8 (behaves like
  plain translucency now), so it's not worth the extra API surface.
- Make the overlay position/size configurable.
- Scroll and key-combo (Ctrl+C etc.) execution types, if the planner ever
  emits them — not in the current `Execution` schema.
