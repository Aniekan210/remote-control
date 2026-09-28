package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// ── event helpers ────────────────────────────────────────────────

func create(desc string) Action {
	return Action{Type: "CREATE_TASK", Description: desc, ReceivedAt: t0}
}

func advance(seq int) Action {
	return Action{Type: "ADVANCE", Seq: seq, ReceivedAt: t0,
		ScreenshotPayload: Screenshot{Format: "jpeg", Width: 10, Height: 10, Data: []byte{1, 2, 3}}}
}

func answer(text string) Action {
	return Action{Type: "ANSWER", Description: text, ReceivedAt: t0}
}

func plan(t Task, steps ...string) AIResult {
	return AIResult{Seq: t.Seq, Planning: true, Plan: PlanResult{
		Revise: isRevise(t), Decision: decisionContinue, Instructions: steps, NeedsConfirm: make([]bool, len(steps)),
	}}
}

func exec(t Task, verdict string, done bool, reason string, actions ...Execution) AIResult {
	return AIResult{Seq: t.Seq, Exec: ExecResult{Verdict: verdict, InstructionDone: done, Reason: reason, Actions: actions}}
}

var click = Execution{Type: "LEFT_CLICK"}

// running returns a task that has been created and planned with steps,
// ready for its first executor ADVANCE.
func running(t *testing.T, steps ...string) Task {
	t.Helper()
	task := onClientAction(resetTask("dev"), create("do it"))
	task = onClientAction(task, advance(task.Seq))
	if !task.InFlight {
		t.Fatalf("first ADVANCE should request the planner call")
	}
	task = onAIResult(task, plan(task, steps...))
	if task.Status != "RUNNING" || task.Context {
		t.Fatalf("after the plan: status=%s context=%v", task.Status, task.Context)
	}
	return task
}

func mustEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// ── scenarios ────────────────────────────────────────────────────

func TestStateMachine(t *testing.T) {
	limits = defaultLimits()

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"happy path", func(t *testing.T) {
			task := onClientAction(resetTask("dev"), create("search cats"))
			mustEqual(t, "status", task.Status, "RUNNING")
			mustEqual(t, "context", task.Context, true)
			mustEqual(t, "seq", task.Seq, 1)
			mustEqual(t, "confirmed", task.ConfirmedIndex, -1)

			task = onClientAction(task, advance(1))
			mustEqual(t, "in flight", task.InFlight, true)
			mustEqual(t, "seq unchanged while the call runs", task.Seq, 1)

			task = onAIResult(task, plan(task, "a", "b"))
			mustEqual(t, "steps", len(task.InstructionList), 2)
			mustEqual(t, "context", task.Context, false)
			mustEqual(t, "seq", task.Seq, 2)
			mustEqual(t, "in flight", task.InFlight, false)

			for i := 0; i < 2; i++ {
				task = onClientAction(task, advance(task.Seq))
				task = onAIResult(task, exec(task, verdictAct, true, "", click))
				mustEqual(t, "index", task.CurrentInstructionIndex, i+1)
				mustEqual(t, "actions", len(task.ExecutionList), 1)
			}

			// Past the last step: the final check, not an instant COMPLETED.
			task = onClientAction(task, advance(task.Seq))
			mustEqual(t, "final check requested", task.InFlight, true)
			mustEqual(t, "still running", task.Status, "RUNNING")
			task = onAIResult(task, exec(task, verdictSkip, true, ""))
			mustEqual(t, "status", task.Status, "COMPLETED")
			mustEqual(t, "calls", task.AICalls, 4)
		}},

		{"skip", func(t *testing.T) {
			task := running(t, "a", "b")
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, exec(task, verdictSkip, true, ""))
			mustEqual(t, "index", task.CurrentInstructionIndex, 1)
			mustEqual(t, "empty list", len(task.ExecutionList), 0)
			mustEqual(t, "context", task.Context, false)
		}},

		{"act with instruction_done=false, then the per-step cap", func(t *testing.T) {
			task := running(t, "a", "b")
			for i := 1; i <= maxExecutorCallsPerStep; i++ {
				seq := task.Seq
				task = onClientAction(task, advance(task.Seq))
				task = onAIResult(task, exec(task, verdictAct, false, "", click))
				mustEqual(t, "index stays", task.CurrentInstructionIndex, 0)
				mustEqual(t, "attempts", task.InstrAttempts, i)
				mustEqual(t, "seq bumped so the worker runs the batch", task.Seq, seq+1)
			}
			task = onClientAction(task, advance(task.Seq))
			mustEqual(t, "no fourth call", task.InFlight, false)
			mustEqual(t, "replan", task.Context, true)
			if !strings.Contains(task.Reason, "wasn't done after 3 attempts") {
				t.Fatalf("reason = %q", task.Reason)
			}
		}},

		{"replan, then the replan cap asks the user", func(t *testing.T) {
			task := running(t, "a", "b")
			for i := 1; i <= limits.MaxAutoReplans; i++ {
				task = onClientAction(task, advance(task.Seq))
				task = onAIResult(task, exec(task, verdictReplan, false, "a popup is in the way"))
				mustEqual(t, "status", task.Status, "RUNNING")
				mustEqual(t, "context", task.Context, true)
				mustEqual(t, "reason", task.Reason, "a popup is in the way")
				mustEqual(t, "auto replans", task.AutoReplans, i)

				// The worker's context ADVANCE makes a revise call.
				task = onClientAction(task, advance(task.Seq))
				mustEqual(t, "revise requested", task.InFlight, true)
				task = onAIResult(task, plan(task, "dismiss the popup", "b"))
				mustEqual(t, "context", task.Context, false)
				mustEqual(t, "reason cleared", task.Reason, "")
			}
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, exec(task, verdictReplan, false, "still a popup"))
			mustEqual(t, "status", task.Status, "NEEDS_INPUT")
			mustEqual(t, "kind", task.QuestionKind, "blocked")
			if !strings.HasPrefix(task.Question, "I've tried to recover 3 times and I'm still stuck: still a popup.") {
				t.Fatalf("question = %q", task.Question)
			}
		}},

		{"worker error becomes a replan", func(t *testing.T) {
			task := running(t, "a")
			a := advance(task.Seq)
			a.Error = "coordinates (5000, 10) are outside the 1920x1080 screen"
			task = onClientAction(task, a)
			mustEqual(t, "no AI call", task.InFlight, false)
			mustEqual(t, "context", task.Context, true)
			if !strings.Contains(task.Reason, "outside the 1920x1080 screen") {
				t.Fatalf("reason = %q", task.Reason)
			}
		}},

		{"blocked -> ANSWER -> revise continue keeps completed steps", func(t *testing.T) {
			task := running(t, "a", "send invite", "c")
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, exec(task, verdictAct, true, "", click)) // a done
			task = onClientAction(task, advance(task.Seq))
			blocked := exec(task, verdictBlocked, false, "You're out of personalized invites.")
			blocked.Image = "data:image/jpeg;base64,AAA"
			task = onAIResult(task, blocked)
			mustEqual(t, "status", task.Status, "NEEDS_INPUT")
			mustEqual(t, "question", task.Question, "You're out of personalized invites.")
			mustEqual(t, "image", task.QuestionImage, "data:image/jpeg;base64,AAA")
			mustEqual(t, "list cleared", len(task.ExecutionList), 0)

			seq := task.Seq
			task = onClientAction(task, answer("send it without a note"))
			mustEqual(t, "status", task.Status, "RUNNING")
			mustEqual(t, "context", task.Context, true)
			mustEqual(t, "reason", task.Reason, "User answered: send it without a note")
			mustEqual(t, "answers", len(task.Answers), 1)
			mustEqual(t, "answer text", task.Answers[0], "Q: You're out of personalized invites. / A: send it without a note")
			mustEqual(t, "question cleared", task.Question+task.QuestionKind+task.QuestionImage, "")
			mustEqual(t, "not an auto replan", task.AutoReplans, 0)
			mustEqual(t, "seq", task.Seq, seq+1)

			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, plan(task, "send the invite without a note", "c"))
			mustEqual(t, "steps", strings.Join(task.InstructionList, "|"), "a|send the invite without a note|c")
			mustEqual(t, "index", task.CurrentInstructionIndex, 1)
			mustEqual(t, "context", task.Context, false)
		}},

		{"revise ask", func(t *testing.T) {
			task := running(t, "a")
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, exec(task, verdictBlocked, false, "Which account?"))
			task = onClientAction(task, answer("the other one"))
			task = onClientAction(task, advance(task.Seq))
			r := AIResult{Seq: task.Seq, Planning: true, Plan: PlanResult{Revise: true, Decision: decisionAsk, Message: "Work or personal?"}}
			task = onAIResult(task, r)
			mustEqual(t, "status", task.Status, "NEEDS_INPUT")
			mustEqual(t, "question", task.Question, "Work or personal?")
			mustEqual(t, "context", task.Context, false)
		}},

		{"revise stop", func(t *testing.T) {
			task := running(t, "a", "b")
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, exec(task, verdictBlocked, false, "Limit reached."))
			task = onClientAction(task, answer("that's fine, skip the rest"))
			task = onClientAction(task, advance(task.Seq))
			r := AIResult{Seq: task.Seq, Planning: true, Plan: PlanResult{Revise: true, Decision: decisionStop, Message: "Skipping the rest."}}
			task = onAIResult(task, r)
			mustEqual(t, "status", task.Status, "COMPLETED")
		}},

		{"first plan may ask", func(t *testing.T) {
			task := onClientAction(resetTask("dev"), create("email him"))
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, AIResult{Seq: task.Seq, Planning: true, Plan: PlanResult{Decision: decisionAsk, Message: "Who is him?"}})
			mustEqual(t, "status", task.Status, "NEEDS_INPUT")
			task = onClientAction(task, answer("Sam"))
			mustEqual(t, "revise next", isRevise(task), true)
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, plan(task, "open gmail", "email Sam"))
			mustEqual(t, "steps", len(task.InstructionList), 2)
			mustEqual(t, "index", task.CurrentInstructionIndex, 0)
		}},

		{"confirm approve", func(t *testing.T) {
			task := running(t, "write it", "send it")
			task.NeedsConfirm = []bool{false, true}
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, exec(task, verdictAct, true, "", click))

			task = onClientAction(task, advance(task.Seq))
			mustEqual(t, "no executor call before approval", task.InFlight, false)
			mustEqual(t, "status", task.Status, "NEEDS_INPUT")
			mustEqual(t, "kind", task.QuestionKind, "confirm")
			mustEqual(t, "question", task.Question, "About to: send it")

			task = onClientAction(task, answer("approve"))
			mustEqual(t, "status", task.Status, "RUNNING")
			mustEqual(t, "confirmed", task.ConfirmedIndex, 1)
			mustEqual(t, "no planner call", task.Context, false)
			mustEqual(t, "no answer recorded", len(task.Answers), 0)

			task = onClientAction(task, advance(task.Seq))
			mustEqual(t, "executor call", task.InFlight, true)
		}},

		{"confirm with other text revises", func(t *testing.T) {
			task := running(t, "send it")
			task.NeedsConfirm = []bool{true}
			task = onClientAction(task, advance(task.Seq))
			task = onClientAction(task, answer("save it as a draft instead"))
			mustEqual(t, "status", task.Status, "RUNNING")
			mustEqual(t, "context", task.Context, true)
			mustEqual(t, "reason", task.Reason, "User answered: save it as a draft instead")
			mustEqual(t, "not approved", task.ConfirmedIndex, -1)
		}},

		{"budget question", func(t *testing.T) {
			task := running(t, "a")
			task.AICalls = limits.MaxAICallsPerTask
			task.CostUSD = 0.05
			task = onClientAction(task, advance(task.Seq))
			mustEqual(t, "no call", task.InFlight, false)
			mustEqual(t, "status", task.Status, "NEEDS_INPUT")
			mustEqual(t, "kind", task.QuestionKind, "budget")
			if !strings.HasPrefix(task.Question, "This task has used 50 AI calls / $0.050") {
				t.Fatalf("question = %q", task.Question)
			}

			task = onClientAction(task, answer("continue"))
			mustEqual(t, "status", task.Status, "RUNNING")
			mustEqual(t, "calls reset", task.AICalls, 0)
			mustEqual(t, "cost window", task.CostBase, 0.05)
			mustEqual(t, "no planner call", task.Context, false)
			task = onClientAction(task, advance(task.Seq))
			mustEqual(t, "carries on", task.InFlight, true)
		}},

		{"duration cap", func(t *testing.T) {
			task := running(t, "a")
			a := advance(task.Seq)
			a.ReceivedAt = t0.Add(limits.MaxTaskDuration)
			task = onClientAction(task, a)
			mustEqual(t, "kind", task.QuestionKind, "budget")
		}},

		{"stale seq", func(t *testing.T) {
			task := running(t, "a")
			before := task
			task = onClientAction(task, advance(task.Seq-1))
			mustEqual(t, "ignored", task.InFlight, false)
			mustEqual(t, "seq", task.Seq, before.Seq)

			// A duplicate ADVANCE while the call runs is ignored too.
			task = onClientAction(task, advance(task.Seq))
			dup := onClientAction(task, advance(task.Seq))
			mustEqual(t, "duplicate ignored", dup.Seq, task.Seq)
			mustEqual(t, "attempts untouched", dup.InstrAttempts, task.InstrAttempts)
		}},

		{"AI result after cancel is dropped", func(t *testing.T) {
			task := running(t, "a")
			task = onClientAction(task, advance(task.Seq))
			pending := exec(task, verdictAct, true, "", click)
			task = onClientAction(task, Action{Type: "CANCEL_TASK"})
			mustEqual(t, "status", task.Status, "NONE")
			after := onAIResult(task, pending)
			mustEqual(t, "still none", after.Status, "NONE")
			mustEqual(t, "no actions", len(after.ExecutionList), 0)
			mustEqual(t, "seq", after.Seq, task.Seq)
		}},

		{"pause and resume", func(t *testing.T) {
			task := running(t, "a")
			seq := task.Seq
			task = onClientAction(task, advance(seq))
			task = onClientAction(task, Action{Type: "PAUSE_TASK"})
			mustEqual(t, "status", task.Status, "PAUSED")
			mustEqual(t, "no seq bump", task.Seq, seq)

			// The call that was in flight lands while paused: dropped.
			task = onAIResult(task, exec(task, verdictAct, true, "", click))
			mustEqual(t, "no actions", len(task.ExecutionList), 0)
			mustEqual(t, "index", task.CurrentInstructionIndex, 0)
			mustEqual(t, "not in flight", task.InFlight, false)

			// ADVANCE while paused is ignored; after resume the same seq works.
			task = onClientAction(task, advance(seq))
			mustEqual(t, "ignored while paused", task.InFlight, false)
			task = onClientAction(task, Action{Type: "RESUME_TASK"})
			mustEqual(t, "status", task.Status, "RUNNING")
			mustEqual(t, "no seq bump", task.Seq, seq)
			task = onClientAction(task, advance(seq))
			mustEqual(t, "fresh call", task.InFlight, true)

			// RESUME when not paused is ignored.
			again := onClientAction(task, Action{Type: "RESUME_TASK"})
			mustEqual(t, "ignored", again.Status, "RUNNING")
		}},

		{"empty plan", func(t *testing.T) {
			task := onClientAction(resetTask("dev"), create("nothing"))
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, plan(task))
			mustEqual(t, "status", task.Status, "COMPLETED")
		}},

		{"ANSWER when not waiting is ignored", func(t *testing.T) {
			task := running(t, "a")
			after := onClientAction(task, answer("hello"))
			mustEqual(t, "status", after.Status, "RUNNING")
			mustEqual(t, "seq", after.Seq, task.Seq)
			mustEqual(t, "answers", len(after.Answers), 0)
		}},

		{"AI failure stops the task with an error", func(t *testing.T) {
			task := running(t, "a")
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, AIResult{Seq: task.Seq, Err: errors.New("503")})
			mustEqual(t, "status", task.Status, "NONE")
			if task.LastError == "" {
				t.Fatal("expected last_error")
			}
		}},

		{"a rejected key says so", func(t *testing.T) {
			task := running(t, "a")
			task = onClientAction(task, advance(task.Seq))
			task = onAIResult(task, AIResult{Seq: task.Seq, Err: errors.New("OpenRouter returned status 401: bad key")})
			mustEqual(t, "error", task.LastError, "OpenRouter rejected the API key. Check it in Settings.")
		}},

		{"refused CREATE_TASK", func(t *testing.T) {
			a := create("x")
			a.Refused = "Add your OpenRouter key in Settings"
			task := onClientAction(resetTask("dev"), a)
			mustEqual(t, "status", task.Status, "NONE")
			mustEqual(t, "error", task.LastError, "Add your OpenRouter key in Settings")
			task = onClientAction(task, create("y"))
			mustEqual(t, "cleared on the next accepted action", task.LastError, "")
			mustEqual(t, "started", task.Status, "RUNNING")
		}},
	}

	for _, c := range cases {
		t.Run(c.name, c.run)
	}
}
