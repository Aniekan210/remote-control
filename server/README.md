# Remote Control server — with logging

Same server you had (`main.go`, `ai.go`, `types.go`), now instrumented end
to end. I built it, ran it, and drove a real `CREATE_TASK` + `ADVANCE`
through it before sending this — the log excerpt below is actual output
from that run, not a mockup.

## What changed

**`ai.go`** — this is where your suspicion pointed, so it got the most
detail. Every call now logs, tagged with a `callID` so concurrent calls
don't interleave into noise (grep the same `callID` to follow one call
start to finish):
- Whether it's a PLANNING or EXECUTION call, and the task/instruction it's for
- The outgoing request: model, size, and for execution calls, the
  screenshot dimensions and byte count (with an explicit warning if either
  is 0 — a blank or missing screenshot is a common silent failure mode
  that otherwise just looks like "the model did something dumb")
- **A warning if `OPENROUTER_API_KEY` is empty**, logged before the
  request even goes out — otherwise every call just fails with a generic
  401/403 that looks identical to a real billing/auth problem
- Request timing and the HTTP status code, always (not just on error)
- Token usage from the response (prompt/completion/total) — the fastest
  way to notice something like a malformed image URL the provider is
  silently ignoring
- The full raw model response, before and after markdown-fence stripping
- The final parsed result (instruction list or execution list), or the
  exact parse error with the raw text that failed to parse
- A warning if a model returns an empty execution list (the task would
  otherwise just silently stop advancing with no explanation)

**`main.go`** — the transport/state layer, tagged `[server]` (vs. `[ai]`
in ai.go, so you can grep either layer independently):
- Every room create/reject, every WS connect/disconnect (with reason)
- Every action received, with type
- Every task status transition (`RUNNING -> NONE`, etc.), logged explicitly
- Every retry attempt for an ADVANCE call, with its own timing
- Every broadcast, with client count

One actual bug fixed along the way: `main()` used to `log.Fatal` and kill
the entire server if `.env` failed to load, for any reason — including on
a machine where the API key is set as a real environment variable instead
of a `.env` file. That's now a loud warning instead of a hard crash.

## Real output from a live run

This is the unedited log from actually running this server here (no
`.env`, so `OPENROUTER_API_KEY` was intentionally empty, to confirm that
path logs clearly instead of just producing a mystery 401 deep in a retry
loop):

```
[server] WS connected: device=smoketest123 remote=127.0.0.1:37176 clientsInRoom=1 currentStatus=NONE
[server] device=smoketest123: received action type=CREATE_TASK
[server] device=smoketest123: CREATE_TASK description="open notepad"
[server] device=smoketest123: status transition NONE -> RUNNING
[server] device=smoketest123: broadcasting status=RUNNING to 1 client(s)
[server] device=smoketest123: received action type=ADVANCE
[server] device=smoketest123: ADVANCE received, context=true currentInstrIdx=0/0 screenshotBytes=68 fsEntries=0
[ai] [smoketest123-...] start mode=PLANNING device=smoketest123 status=RUNNING instrIdx=0/0
[ai] [smoketest123-...] planning request: task="" filesystemEntries=0
[ai] [smoketest123-...] WARNING: OPENROUTER_API_KEY is empty — every request will fail auth
[ai] [smoketest123-...] calling OpenRouter: model=deepseek/deepseek-v4.1-flash requestBytes=3517
[ai] [smoketest123-...] FAILED request after 210.321573ms: Post "https://openrouter.ai/api/v1/chat/completions": Forbidden
[server] device=smoketest123: sendMessage attempt 1/3 FAILED after 210.514059ms: ...
[server] device=smoketest123: retrying in 1s
... (2 more retries, same shape) ...
[server] device=smoketest123: giving up after 3 attempts, resetting task to NONE: ...
[server] device=smoketest123: status transition RUNNING -> NONE
[server] device=smoketest123: broadcasting status=NONE to 1 client(s)
```

That's the exact failure mode that would otherwise look like "the AI just
isn't doing anything" — now the very first log line after the action
tells you why.

## Build

```
go mod tidy
go build -o server .
./server
```

Set `OPENROUTER_API_KEY` (via `.env` or your real environment) before
running for real — the log above shows exactly what happens if you don't.

## What to send me if you still see a problem

Run your actual repro, then paste the block of `[ai]` lines for that one
`callID` (they're grep-able together) — specifically I want to see:
1. Whether `screenshotBytes` is a real number or 0/suspiciously small
2. The `raw model content:` line — this shows you exactly what the model
   returned before any parsing touched it, which settles whether a
   problem is the model returning something wrong vs. our code
   misinterpreting something valid
3. The `usage:` line, to rule out the image not actually reaching the model
