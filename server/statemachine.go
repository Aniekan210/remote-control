package main

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// The task state machine. Every state change a task goes through happens
// in one of the two functions below, purely: they take the current Task
// and an event and return the next Task. No network, no mutex, no clock
// (times arrive inside the events) — main.go does the locking, the AI
// calls and the broadcasting, which is what makes all of this testable
// (statemachine_test.go).
//
// The protocol invariant: the server only ever broadcasts the full Task,
// and the worker and the web client each work out what to do from it
// alone — Status, Context and ExecutionList for the worker (see
// desktop-worker/executor.go RunExecutor), plus the question fields for
// the web client. Seq is bumped on every transition the worker must act
// on; the worker echoes it in ADVANCE so stale ADVANCEs and stale AI
// results can be told apart.

// AIResult is the outcome of the AI call an ADVANCE asked for.
type AIResult struct {
	Seq      int           // the Task.Seq the call was made for
	Planning bool          // a planner call (else an executor call)
	Plan     PlanResult    // for a planner call
	Exec     ExecResult    // for an executor call
	Err      error         // the call failed (after its retries)
	Cost     float64       // what the call (all attempts) cost, USD
	Image    string        // the ADVANCE's screenshot as a data URL, for any question
	Attempts int           // informational: how many attempts were made
	Took     time.Duration // informational
}

// onClientAction applies an action from a connection (the worker or a web
// client) to the task. For an ADVANCE that needs an AI call it sets
// InFlight and returns: main makes the call and feeds the outcome to
// onAIResult. Actions that make no sense in the current state are logged
// and ignored (the task comes back unchanged).
//
// main sets two server-side fields on the Action first: ReceivedAt (the
// clock) and, for CREATE_TASK, Refused (why the task can't start — no key,
// monthly budget spent — found with I/O this function mustn't do).
func onClientAction(t Task, a Action) Task {
	switch a.Type {
	case "CREATE_TASK", "PAUSE_TASK", "RESUME_TASK", "CANCEL_TASK", "ANSWER":
		// A one-off error is shown until the next accepted client action.
		t.LastError = ""
	}

	switch a.Type {
	case "CREATE_TASK":
		if a.Refused != "" {
			t.LastError = a.Refused
			srvLogf("device=%s: CREATE_TASK refused: %s", t.DeviceID, a.Refused)
			return t
		}
		// Full reset, not just overwrite: a device that ran a previous
		// task (completed, paused, or otherwise not explicitly
		// CANCEL_TASK'd) still has that task's leftover
		// CurrentInstructionIndex/InstructionList/ExecutionList sitting
		// in taskHashTable — CREATE_TASK used to only touch
		// Description/Context/Status, so a stale non-empty
		// ExecutionList would survive into the new task. The worker's
		// executor treats "ExecutionList non-empty" as "re-run these
		// actions" unconditionally, so it would replay the OLD task's
		// last batch of clicks/keystrokes instead of sending the
		// ADVANCE that starts planning the NEW task — which looks
		// exactly like "ADVANCE is never sent" from the server's side,
		// since the worker is busy doing something else instead.
		next := resetTask(t.DeviceID)
		next.Description = a.Description
		next.Status = "RUNNING"
		next.Context = true
		next.StartedAt = a.ReceivedAt
		next.Seq = t.Seq + 1
		srvLogf("device=%s: CREATE_TASK description=%q (full state reset)", t.DeviceID, a.Description)
		return next

	case "ADVANCE":
		return onAdvance(t, a)

	case "PAUSE_TASK":
		if t.Status != "RUNNING" {
			srvLogf("device=%s: ignoring PAUSE_TASK, task is %s", t.DeviceID, t.Status)
			return t
		}
		t.Status = "PAUSED"
		srvLogf("device=%s: PAUSE_TASK", t.DeviceID)
		return t

	case "RESUME_TASK":
		if t.Status != "PAUSED" {
			srvLogf("device=%s: ignoring RESUME_TASK, task is %s", t.DeviceID, t.Status)
			return t
		}
		// No Seq bump: the worker's state is the same as before the
		// pause, and it re-sends its ADVANCE for this Seq (without
		// replaying actions) when it sees RUNNING again.
		t.Status = "RUNNING"
		srvLogf("device=%s: RESUME_TASK", t.DeviceID)
		return t

	case "ANSWER":
		if t.Status != "NEEDS_INPUT" {
			srvLogf("device=%s: ignoring ANSWER, task is %s (not waiting on a question)", t.DeviceID, t.Status)
			return t
		}
		answer := strings.TrimSpace(a.Description)
		if answer == "" && t.QuestionKind != "budget" {
			srvLogf("device=%s: ignoring empty ANSWER", t.DeviceID)
			return t
		}
		srvLogf("device=%s: ANSWER to %s question %q: %q", t.DeviceID, t.QuestionKind, t.Question, answer)
		t = applyAnswer(t, answer, a.ReceivedAt)
		t.Seq++
		return t

	case "CANCEL_TASK":
		next := resetTask(t.DeviceID)
		next.Seq = t.Seq + 1
		srvLogf("device=%s: CANCEL_TASK, task reset", t.DeviceID)
		return next
	}

	srvLogf("device=%s: unknown action type: %q", t.DeviceID, a.Type)
	return t
}

// aiErrorMessage turns an OpenRouter error that retrying can't fix into a
// message for the user, or "" for anything else.
func aiErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "status 401"), strings.Contains(msg, "status 403"):
		return "OpenRouter rejected the API key. Check it in Settings."
	case strings.Contains(msg, "status 402"):
		return "The OpenRouter account is out of credits (or hit its key's credit limit)."
	}
	return ""
}

// onAdvance handles the worker's ADVANCE: either a transition that needs
// no AI (a worker error, a step to approve, a cap reached, a step stuck
// too long) or a request for the next AI call (InFlight).
func onAdvance(t Task, a Action) Task {
	srvLogf("device=%s: ADVANCE received, seq=%d (task seq=%d) context=%v currentInstrIdx=%d/%d screenshotBytes=%d fsEntries=%d",
		t.DeviceID, a.Seq, t.Seq, t.Context, t.CurrentInstructionIndex, len(t.InstructionList),
		len(a.ScreenshotPayload.Data), len(a.FileSystemPayload))

	// Only an ADVANCE for the current state counts, once (E9). Without
	// this, two in-flight ADVANCEs (e.g. around a reconnect) both advanced
	// the task and skipped a step. Seq 0 is a worker from before sequence
	// numbers: accepted, but still de-duplicated by InFlight.
	switch {
	case t.Status != "RUNNING":
		srvLogf("device=%s: ignoring ADVANCE, task is %s", t.DeviceID, t.Status)
		return t
	case a.Seq != 0 && a.Seq != t.Seq:
		srvLogf("device=%s: ignoring stale ADVANCE (seq %d, task is at %d)", t.DeviceID, a.Seq, t.Seq)
		return t
	case t.InFlight:
		srvLogf("device=%s: ignoring duplicate ADVANCE, an AI call for seq %d is already running", t.DeviceID, t.Seq)
		return t
	}

	image := screenshotDataURL(a.ScreenshotPayload)

	switch {
	case a.Error != "":
		// The worker couldn't do what it was told (the screenshot failed
		// twice, coordinates off the screen, an action type it doesn't
		// know). That's the screen not matching the plan, as far as
		// recovery goes: revise it, no executor call.
		srvLogf("device=%s: worker reported an error, replanning: %s", t.DeviceID, a.Error)
		t = requestReplan(t, "The computer couldn't carry out the last step: "+a.Error, image)

	case needsConfirmation(t):
		// Irreversible steps (send, submit, delete, purchase, post, close
		// unsaved work) wait for the user's approval before the executor
		// even looks at them. No AI call for the question.
		step := t.InstructionList[t.CurrentInstructionIndex]
		srvLogf("device=%s: step %d needs approval before it runs: %q", t.DeviceID, t.CurrentInstructionIndex+1, step)
		t = askUser(t, "confirm", "About to: "+step, image)

	case taskCapReached(t, a.ReceivedAt) != "":
		// Per-task caps: a runaway task (looping, or just long) stops
		// spending before the next call. Hitting a cap doesn't fail the
		// task — it becomes a budget question, and answering it grants a
		// fresh window of calls/spend/time.
		reason := taskCapReached(t, a.ReceivedAt)
		srvLogf("device=%s: per-task cap reached, asking the user: %s", t.DeviceID, reason)
		t = askUser(t, "budget", reason, image)

	case !t.Context && t.InstrAttempts >= maxExecutorCallsPerStep:
		// A step that still isn't done after maxExecutorCallsPerStep
		// executor calls won't be fixed by another: the plan is off.
		srvLogf("device=%s: instruction %d not done after %d executor calls, replanning",
			t.DeviceID, t.CurrentInstructionIndex, t.InstrAttempts)
		reason := fmt.Sprintf("The task still wasn't complete after %d final checks.", t.InstrAttempts)
		if t.CurrentInstructionIndex < len(t.InstructionList) {
			reason = fmt.Sprintf("Step %d (%q) still wasn't done after %d attempts.",
				t.CurrentInstructionIndex+1, t.InstructionList[t.CurrentInstructionIndex], t.InstrAttempts)
		}
		t = requestReplan(t, reason, image)

	default:
		// The next AI call: the planner when Context is set (a first plan
		// or a revise), else the executor — past the last step that's the
		// final check, since a task is only COMPLETED once the executor
		// has looked at the screen and agrees it's done (E8).
		if !t.Context && t.CurrentInstructionIndex >= len(t.InstructionList) {
			srvLogf("device=%s: all instructions executed, running the final check", t.DeviceID)
		}
		t.InFlight = true
		return t // no Seq bump: nothing for the worker to do until the result
	}

	t.Seq++
	return t
}

// onAIResult applies the outcome of an AI call. A result for a Seq the
// task has moved past (cancelled, answered, replaced) is dropped; so is
// one that arrives while the task is paused — the screen may have changed
// under it — in which case the worker re-sends its ADVANCE on resume.
func onAIResult(t Task, r AIResult) Task {
	if r.Seq != t.Seq {
		srvLogf("device=%s: dropping stale AI result for seq %d, task is now at seq %d (%s)",
			t.DeviceID, r.Seq, t.Seq, t.Status)
		return t
	}

	t.InFlight = false
	t.CostUSD += r.Cost
	t.AICalls++
	if r.Planning {
		t.PlannerCalls++
	}

	switch {
	case r.Err != nil:
		// Never remaking a room, so give up by resetting the task to a
		// clean slate instead of deleting it from the table.
		srvLogf("device=%s: giving up after repeated AI failures, resetting task to NONE: %v", t.DeviceID, r.Err)
		next := resetTask(t.DeviceID)
		next.Seq = t.Seq + 1
		next.LastError = "The AI calls kept failing, so the task was stopped. Try again."
		if msg := aiErrorMessage(r.Err); msg != "" {
			next.LastError = msg
		}
		return next

	case t.Status != "RUNNING":
		srvLogf("device=%s: dropping AI result, task was %s while the call was in flight", t.DeviceID, t.Status)
		return t

	case r.Planning:
		t = applyPlan(t, r.Plan, r.Image)

	default:
		t = applyExecResult(t, r.Exec, r.Image)
	}

	t.Seq++
	return t
}

// applyPlan applies the planner's answer.
//   - continue: a first plan replaces the (empty) list; a revise keeps the
//     completed steps (list[:idx]) and replaces everything after with the
//     new remaining steps. Either way execution carries on at idx.
//   - ask:      the planner can't decide safely — ask the user (the answer
//     comes back through a revise)
//   - stop:     nothing more to do — COMPLETED
func applyPlan(t Task, p PlanResult, image string) Task {
	switch p.Decision {
	case decisionAsk:
		srvLogf("device=%s: planner asks the user: %s", t.DeviceID, p.Message)
		t.Context = false
		t.Reason = ""
		return askUser(t, "blocked", p.Message, image)

	case decisionStop:
		srvLogf("device=%s: planner says stop: %s — task COMPLETED", t.DeviceID, p.Message)
		t.Context = false
		t.Reason = ""
		t.ExecutionList = make([]Execution, 0)
		t.Status = "COMPLETED"
		return t
	}

	idx := 0
	if p.Revise {
		idx = min(max(t.CurrentInstructionIndex, 0), len(t.InstructionList))
	}
	completed := append([]string{}, t.InstructionList[:idx]...)
	completedConfirm := make([]bool, idx)
	copy(completedConfirm, t.NeedsConfirm[:min(idx, len(t.NeedsConfirm))])

	t.InstructionList = append(completed, p.Instructions...)
	t.NeedsConfirm = append(completedConfirm, p.NeedsConfirm...)
	t.CurrentInstructionIndex = idx
	t.Context = false // next ADVANCE generates executions, not a new plan
	t.Reason = ""
	t.InstrAttempts = 0
	t.ExecutionList = make([]Execution, 0)
	if t.ConfirmedIndex >= idx {
		// The steps from idx on are new; an approval was for the old ones.
		t.ConfirmedIndex = -1
	}
	srvLogf("device=%s: plan set (revise=%v), %d completed + %d new instructions: %v",
		t.DeviceID, p.Revise, idx, len(p.Instructions), p.Instructions)

	// An empty first plan means there's nothing to execute. Mark the task
	// COMPLETED instead of leaving it RUNNING with 0 instructions —
	// otherwise the next ADVANCE asks for instruction 0 of a 0-length list
	// and fails ("index out of range") on a loop until the retries give up.
	if len(t.InstructionList) == 0 {
		t.Status = "COMPLETED"
		srvLogf("device=%s: planner returned an empty plan, marking COMPLETED", t.DeviceID)
	}
	return t
}

// maxExecutorCallsPerStep caps executor calls on one instruction (an "act"
// with instruction_done=false asks for another look); past it, replan.
const maxExecutorCallsPerStep = 3

// applyExecResult applies the executor's verdict for the current step:
//   - act:     hand the worker the actions; move to the next step only if
//     the executor says they finish this one (otherwise the next ADVANCE
//     gets another executor call on the same step)
//   - skip:    the step is already done — next step, nothing to run (the
//     worker sees an empty list and just sends a plain ADVANCE)
//   - replan:  off-plan but achievable — revise the plan
//   - blocked: can't be done as asked — ask the user
//
// image is the screenshot the executor judged, as a data URL, shown with
// any question that results.
//
// Past the last step the call was the final check (E8): skip means the
// whole task is done (COMPLETED); act runs its fix-up actions and checks
// again (capped like any step); replan/blocked as usual.
func applyExecResult(t Task, res ExecResult, image string) Task {
	if t.CurrentInstructionIndex >= len(t.InstructionList) {
		switch res.Verdict {
		case verdictSkip:
			t.Status = "COMPLETED"
			t.ExecutionList = make([]Execution, 0)
			srvLogf("device=%s: final check passed, task COMPLETED", t.DeviceID)
			return t
		case verdictAct:
			t.ExecutionList = res.Actions
			t.InstrAttempts++
			srvLogf("device=%s: final check found the task unfinished, running %d fix-up actions: %+v",
				t.DeviceID, len(res.Actions), res.Actions)
			return t
		}
	}

	switch res.Verdict {
	case verdictAct:
		t.ExecutionList = res.Actions
		if res.InstructionDone {
			t.CurrentInstructionIndex++
			t.InstrAttempts = 0
		} else {
			t.InstrAttempts++
		}
		srvLogf("device=%s: executor act: %d actions for instruction %d/%d (done=%v): %+v",
			t.DeviceID, len(res.Actions), t.CurrentInstructionIndex, len(t.InstructionList), res.InstructionDone, res.Actions)

	case verdictSkip:
		t.ExecutionList = make([]Execution, 0)
		t.CurrentInstructionIndex++
		t.InstrAttempts = 0
		srvLogf("device=%s: executor skip: instruction already done on screen, now %d/%d",
			t.DeviceID, t.CurrentInstructionIndex, len(t.InstructionList))

	case verdictReplan:
		srvLogf("device=%s: executor replan: %s", t.DeviceID, res.Reason)
		t = requestReplan(t, res.Reason, image)

	case verdictBlocked:
		srvLogf("device=%s: executor blocked: %s", t.DeviceID, res.Reason)
		t = askUser(t, "blocked", res.Reason, image)
	}
	return t
}

// requestReplan asks the worker for fresh context so the planner can
// revise the plan: RUNNING with an empty list and Context=true makes the
// worker send a context ADVANCE (screenshot + filesystem), and the planner
// then revises. Automatic revises are capped (MAX_AUTO_REPLANS): past the
// cap the system is going in circles, so it asks the user instead.
func requestReplan(t Task, reason, image string) Task {
	if t.AutoReplans+1 > limits.MaxAutoReplans {
		srvLogf("device=%s: %d automatic revises already, asking the user instead", t.DeviceID, t.AutoReplans)
		return askUser(t, "blocked", fmt.Sprintf(
			"I've tried to recover %d times and I'm still stuck: %s What should I do?",
			t.AutoReplans, sentence(reason)), image)
	}
	t.AutoReplans++
	t.Context = true
	t.ExecutionList = make([]Execution, 0)
	t.Reason = reason
	t.InstrAttempts = 0
	t.Status = "RUNNING"
	return t
}

// sentence trims s and makes sure it ends like a sentence.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && !strings.ContainsAny(s[len(s)-1:], ".!?") {
		s += "."
	}
	return s
}

// askUser parks the task on a question for the user. The worker idles
// (Status != RUNNING) until an answer or a cancel arrives. image is the
// screen the question is about, as a data URL ("" if there's none).
func askUser(t Task, kind, question, image string) Task {
	t.Status = "NEEDS_INPUT"
	t.Question = question
	t.QuestionKind = kind
	t.QuestionImage = image
	t.ExecutionList = make([]Execution, 0)
	return t
}

// needsConfirmation reports whether the current step is flagged as
// irreversible and hasn't been approved yet.
func needsConfirmation(t Task) bool {
	idx := t.CurrentInstructionIndex
	return !t.Context && idx >= 0 && idx < len(t.InstructionList) &&
		idx < len(t.NeedsConfirm) && t.NeedsConfirm[idx] && t.ConfirmedIndex != idx
}

// approveAnswer is what the Approve button sends for a confirm question.
const approveAnswer = "approve"

// applyAnswer resumes a NEEDS_INPUT task with the user's answer:
//   - confirm + "approve": the step is approved (ConfirmedIndex) and runs
//     next — plain ADVANCE, executor call, no planner call
//   - budget: any answer means "continue" — the per-task counters start a
//     fresh window and the task carries on where it was, no planner call
//     (Context keeps its value, so a plan that was about to be made still
//     gets made)
//   - blocked, or confirm with any other text ("do something else
//     instead"): the answer is recorded and the planner
//     revises the plan with it — Reason set, Context=true, so the worker
//     sends a context ADVANCE. This doesn't count as an automatic revise.
func applyAnswer(t Task, answer string, now time.Time) Task {
	kind := t.QuestionKind
	question := t.Question

	t.Question = ""
	t.QuestionKind = ""
	t.QuestionImage = ""
	t.ExecutionList = make([]Execution, 0)
	t.Status = "RUNNING"

	switch {
	case kind == "confirm" && strings.EqualFold(answer, approveAnswer):
		t.ConfirmedIndex = t.CurrentInstructionIndex
		t.Context = false
		srvLogf("device=%s: step %d approved by the user", t.DeviceID, t.CurrentInstructionIndex+1)

	case kind == "budget":
		t.AICalls = 0
		t.PlannerCalls = 0
		t.CostBase = t.CostUSD
		t.StartedAt = now
		srvLogf("device=%s: per-task caps reset after the user chose to continue", t.DeviceID)

	default:
		t.Answers = append(t.Answers, fmt.Sprintf("Q: %s / A: %s", question, answer))
		t.Reason = "User answered: " + answer
		t.Context = true
		t.InstrAttempts = 0
	}
	return t
}

// screenshotDataURL renders a screenshot as a data: URL for question_image.
func screenshotDataURL(shot Screenshot) string {
	if len(shot.Data) == 0 {
		return ""
	}
	return "data:image/" + shot.Format + ";base64," + base64.StdEncoding.EncodeToString(shot.Data)
}

// taskCapReached returns a user-facing explanation when the task has hit
// one of the per-task caps (calls, planner calls, spend, running time), or
// "" when it may make another AI call. The planner cap only applies when
// the next call is a planner call (Context == true).
func taskCapReached(t Task, now time.Time) string {
	spent := t.CostUSD - t.CostBase
	used := fmt.Sprintf("This task has used %d AI calls / $%.3f", t.AICalls, t.CostUSD)
	switch {
	case t.AICalls >= limits.MaxAICallsPerTask:
		return used + fmt.Sprintf(" (limit %d calls). Continue?", limits.MaxAICallsPerTask)
	case t.Context && t.PlannerCalls >= limits.MaxPlannerCallsPerTask:
		return used + fmt.Sprintf(" and planned %d times (limit %d). Continue?", t.PlannerCalls, limits.MaxPlannerCallsPerTask)
	case spent >= limits.MaxTaskCostUSD:
		return used + fmt.Sprintf(" (limit $%.2f). Continue?", limits.MaxTaskCostUSD)
	case !t.StartedAt.IsZero() && now.Sub(t.StartedAt) >= limits.MaxTaskDuration:
		return used + fmt.Sprintf(" and has run for %s (limit %s). Continue?",
			now.Sub(t.StartedAt).Round(time.Second), limits.MaxTaskDuration)
	}
	return ""
}

// resetTask returns a fresh, empty task for the given device — the same
// shape CANCEL_TASK produces. Used both for explicit cancellation and for
// giving up on a task after repeated ADVANCE failures: the room's entry in
// taskHashTable stays put (rooms are never recreated), it just goes back
// to a clean NONE state.
func resetTask(deviceID string) Task {
	return Task{
		DeviceID:                deviceID,
		Description:             "",
		Status:                  "NONE",
		CurrentInstructionIndex: 0,
		InstructionList:         make([]string, 0),
		ExecutionList:           make([]Execution, 0),
		Context:                 false,
		Answers:                 make([]string, 0),
		ConfirmedIndex:          -1,
		NeedsConfirm:            make([]bool, 0),
	}
}
