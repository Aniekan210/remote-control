# Remote Control — web

Next.js 16 (App Router) control center: Google sign-in (better-auth), Neon Postgres,
one linked computer per account, live task console over WebSocket, voice input,
QR-code device linking.

## Setup (≈10 min)

```bash
npm install
cp .env.example .env.local        # fill it in — see comments inside
psql "$DATABASE_URL" -f schema.sql # or paste schema.sql into Neon's SQL editor
npm run dev
```

**Google OAuth:** Google Cloud Console → APIs & Services → Credentials → *Create OAuth client ID* → Web application.
- Authorized JavaScript origin: `http://localhost:3000`
- Authorized redirect URI: `http://localhost:3000/api/auth/callback/google`
- Add the same two for your prod domain when you deploy, and set `BETTER_AUTH_URL` to it.

**Testing on your phone in dev:** the phone opens the WebSocket itself, so `CONTROL_WS_URL`
must be reachable *from the phone* — `localhost` won't be. The camera (QR scan) and mic
(speech-to-text) also only work over **HTTPS**. Easiest: tunnel both (`ngrok http 3000`,
`ngrok http 8080`), set `BETTER_AUTH_URL` to the web tunnel, `CONTROL_WS_URL=wss://<go-tunnel>`,
and add the web tunnel to Google's allowed origins/redirects.

## How it fits together

```
worker ──(creates room, connects)──► Go server ◄──(wss /ws?id=<deviceId>)── phone
                                                        ▲
                         Next.js: /api/connect resolves the signed-in user's device → WS URL
```

- The web app **never creates rooms** — the worker does. Until the worker is up, the Go server
  refuses the connection; the console shows "Your computer isn't connected" and keeps retrying
  (backoff capped at 8s, instant retry when the tab comes back to the foreground).
- The browser only sends `CREATE_TASK`, `PAUSE_TASK`, `RESUME_TASK`, `CANCEL_TASK` and renders
  every `TASK_UPDATE`. `ADVANCE` is the worker's job.

### Live transcript (what you see while a task runs)
The console shows the task as a transcript: your request, the plan, then every step with the
actual actions sent to your computer ("Clicked at 412, 88", "Pressed Ctrl + C", "Typed “hi”"),
per-step timings, who paused/resumed, and how it ended. A sticky header keeps step X/N, elapsed
time and a segmented progress bar visible while you scroll.

The Go server only broadcasts the *current* snapshot and overwrites `ExecutionList` each step,
so `src/lib/timeline.ts` rebuilds history on the client by diffing snapshots and timestamping
changes. It's saved in `localStorage` per device, so a reload keeps it. A phone that opens
mid-task sees full detail from that point on; earlier steps show as done without their actions.

Mapping used (from `main.go`): after an execution `ADVANCE`, `ExecutionList` belongs to
`InstructionList[CurrentInstructionIndex - 1]`.

### One task at a time
The UI is the gate — the bottom dock changes with the task's status, so there's never a way to
send an action the current state doesn't allow:

| Status | Dock shows | Can send |
|---|---|---|
| `NONE` | task input + mic | `CREATE_TASK` |
| `RUNNING` | Pause · Cancel | `PAUSE_TASK`, `CANCEL_TASK` |
| `PAUSED` | Resume · Cancel | `RESUME_TASK`, `CANCEL_TASK` |
| `COMPLETED` | New task | `CANCEL_TASK` (clears it back to `NONE`) |

`act()` in `console.tsx` also re-checks against the latest status and blocks everything while
a sent action is waiting for its `TASK_UPDATE`, so a double-tap can't fire two creates.

### Linking a device (QR)
Settings → **Scan QR code** opens the camera. It uses the native `BarcodeDetector` where it
exists (Chrome/Android) and falls back to jsQR (iOS Safari). The QR can contain any of:
- the raw device ID
- a link like `https://<your-app>/settings?device=<id>` — **recommended**: then the phone's
  normal camera app works too; it opens the web app with the ID prefilled (and survives the
  Google sign-in redirect)
- JSON with a `deviceId`/`DeviceID`/`device_id`/`id` field

Manual entry is always there as a fallback.

### Files
| Path | What |
|---|---|
| `schema.sql` | better-auth tables + `device` (PK on `user_id` = one device per user; `UNIQUE device_id` = one account per device) |
| `src/lib/auth.ts` | better-auth config (Google only) |
| `src/lib/device.ts` | device queries |
| `src/lib/device-id.ts` | ID validation + pulling an ID out of QR contents |
| `src/lib/task.ts` | Task type + tolerant parser for the Go JSON |
| `src/lib/timeline.ts` | builds step/action history from snapshots |
| `src/lib/actions.ts` | turns raw executions into readable lines |
| `src/components/use-control-socket.ts` | WebSocket lifecycle |
| `src/components/use-speech.ts` | Web Speech API dictation |
| `src/components/qr-scanner.tsx` | camera QR scanner |
| `src/components/console.tsx` | main screen + status → action gate |
| `src/components/transcript.tsx` | the live transcript |
| `src/components/task-summary.tsx` | sticky progress header |
| `src/app/settings/*` | scan/link, replace, unlink device, sign out |

## Assumptions about the Go side
Only `main.go` was visible, not the structs:
1. **JSON field names** — `src/lib/task.ts` accepts PascalCase / camelCase / snake_case, so it
   works with or without json tags. Outgoing messages are `{"type","description"}`; Go matches
   keys case-insensitively, so they land on `Action.Type` / `Action.Description`.
2. **`Execution` shape** — unknown, so `src/lib/actions.ts` matches common field names (type/action, x/y, text, keys, direction…) and falls back to `type key=value`. Every step also has a **Raw** toggle showing the exact JSON.
