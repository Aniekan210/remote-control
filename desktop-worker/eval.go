//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// The evaluation harness (F7): `remoteworker -eval eval\tasks.json` runs a
// fixed set of everyday tasks end to end — through the real server and the
// real models, on this machine — and prints pass/fail, AI calls, revises,
// questions and cost for each. Use it to compare models (PLANNING_MODEL /
// EXECUTION_MODEL on the server) and prompt changes by measurement.
//
// It costs real money (every task is several AI calls), so run it
// deliberately. The task being evaluated drives this computer's mouse and
// keyboard like any other task.

type evalFile struct {
	TimeoutSeconds int        `json:"timeout_seconds"`
	Tasks          []evalTask `json:"tasks"`
}

type evalTask struct {
	Name                string         `json:"name"`
	Description         string         `json:"description"`
	ExpectStatus        string         `json:"expect_status"`
	WindowTitleContains string         `json:"window_title_contains"`
	FileExists          string         `json:"file_exists"`
	FileContains        *evalFileText  `json:"file_contains"`
	MaxQuestions        *int           `json:"max_questions"`
	SetupFiles          []evalFileText `json:"setup_files"`
	CleanupFiles        []string       `json:"cleanup_files"`
}

type evalFileText struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

type evalResult struct {
	name      string
	pass      bool
	why       string
	status    string
	advances  int64
	revises   int
	questions int
	cost      float64
	took      time.Duration
}

// evalf prints eval output to the console and to worker.log (the release
// build has no console, so the log is where the results end up there).
func evalf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Print(msg)
	if line := strings.TrimSpace(msg); line != "" {
		log.Print("eval: " + line)
	}
}

// advancesSent counts ADVANCEs sent (each is, at most, one AI call).
var advancesSent atomic.Int64

// RunEval runs every task in the file and prints a summary. deviceID is
// this worker's room.
func RunEval(path string, state *State, deviceID string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("eval: can't read %s: %v", path, err)
		return
	}
	var f evalFile
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("eval: can't parse %s: %v", path, err)
		return
	}
	timeout := time.Duration(f.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	// Wait for the connection before starting.
	for state.GetConn() == nil {
		time.Sleep(500 * time.Millisecond)
	}
	evalf("\nEVAL: %d tasks from %s (timeout %s each). This spends real OpenRouter credit.\n\n", len(f.Tasks), path, timeout)

	var results []evalResult
	for _, t := range f.Tasks {
		evalf("── %s: %q\n", t.Name, t.Description)
		r := runEvalTask(t, state, deviceID, timeout)
		results = append(results, r)
		verdict := "PASS"
		if !r.pass {
			verdict = "FAIL (" + r.why + ")"
		}
		evalf("   %s  status=%s advances=%d revises=%d questions=%d cost=$%.4f time=%s\n\n",
			verdict, r.status, r.advances, r.revises, r.questions, r.cost, r.took.Round(time.Second))
	}

	passed := 0
	var cost float64
	evalf("EVAL SUMMARY\n")
	evalf("%-30s %-5s %-12s %8s %8s %9s %9s\n", "task", "ok", "status", "advances", "revises", "questions", "cost")
	for _, r := range results {
		ok := "FAIL"
		if r.pass {
			ok = "PASS"
			passed++
		}
		cost += r.cost
		evalf("%-30s %-5s %-12s %8d %8d %9d %9s\n", r.name, ok, r.status, r.advances, r.revises, r.questions, fmt.Sprintf("$%.4f", r.cost))
	}
	evalf("\n%d/%d passed, total cost $%.4f (advances ≈ AI calls; questions exclude auto-approved confirmations)\n", passed, len(results), cost)
}

func runEvalTask(t evalTask, state *State, deviceID string, timeout time.Duration) evalResult {
	res := evalResult{name: t.Name}
	defer func() {
		for _, p := range t.CleanupFiles {
			_ = os.Remove(expandHome(p))
		}
	}()
	for _, sf := range t.SetupFiles {
		p := expandHome(sf.Path)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(sf.Text), 0o644); err != nil {
			res.why = "setup failed: " + err.Error()
			return res
		}
	}

	// Start from a clean room.
	if st := state.LastTask().Status; st != "NONE" {
		SendAction(state, Action{Type: "CANCEL_TASK", DeviceID: deviceID})
		waitFor(state, 15*time.Second, func(t Task) bool { return t.Status == "NONE" })
	}

	startSeq := state.LastTask().Seq
	startAdv := advancesSent.Load()
	start := time.Now()
	SendAction(state, Action{Type: "CREATE_TASK", DeviceID: deviceID, Description: t.Description})

	seenRevise := map[int]bool{}
	lastStatus := ""
	deadline := time.Now().Add(timeout)
	var final Task
	for time.Now().Before(deadline) {
		cur := state.LastTask()
		if cur.Seq <= startSeq {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if cur.Context && (cur.Reason != "" || len(cur.InstructionList) > 0) && !seenRevise[cur.Seq] {
			seenRevise[cur.Seq] = true
			res.revises++
		}
		if cur.Status == "NEEDS_INPUT" && lastStatus != "NEEDS_INPUT" {
			if cur.QuestionKind == "confirm" && t.ExpectStatus != "NEEDS_INPUT" {
				// Approving irreversible steps is the user's job; the eval
				// plays the user and approves, without counting it.
				evalf("   (auto-approving: %s)\n", cur.Question)
				SendAction(state, Action{Type: "ANSWER", DeviceID: deviceID, Description: "approve"})
			} else {
				res.questions++
				evalf("   question (%s): %s\n", cur.QuestionKind, cur.Question)
				final = cur
				break
			}
		}
		lastStatus = cur.Status
		if cur.Status == "COMPLETED" || cur.Status == "NONE" {
			final = cur
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if final.Status == "" {
		final = state.LastTask()
		res.status = final.Status + " (timeout)"
	} else {
		res.status = final.Status
	}
	res.took = time.Since(start)
	res.advances = advancesSent.Load() - startAdv
	res.cost = final.CostUSD

	res.pass, res.why = checkEval(t, final, res.questions)
	if final.Status == "NONE" && final.LastError != "" {
		res.why += " — server: " + final.LastError
	}

	// Leave the room clean for the next task.
	SendAction(state, Action{Type: "CANCEL_TASK", DeviceID: deviceID})
	waitFor(state, 15*time.Second, func(t Task) bool { return t.Status == "NONE" })
	return res
}

// checkEval applies a task's automatic checks to how it ended.
func checkEval(t evalTask, final Task, questions int) (bool, string) {
	want := t.ExpectStatus
	if want == "" {
		want = "COMPLETED"
	}
	if final.Status != want {
		return false, fmt.Sprintf("ended %s, expected %s", final.Status, want)
	}
	if t.MaxQuestions != nil && questions > *t.MaxQuestions {
		return false, fmt.Sprintf("asked %d question(s), allowed %d", questions, *t.MaxQuestions)
	}
	if t.WindowTitleContains != "" {
		title := foregroundWindowTitle()
		if !strings.Contains(strings.ToLower(title), strings.ToLower(t.WindowTitleContains)) {
			return false, fmt.Sprintf("foreground window %q doesn't contain %q", title, t.WindowTitleContains)
		}
	}
	if t.FileExists != "" {
		if _, err := os.Stat(expandHome(t.FileExists)); err != nil {
			return false, "missing file " + t.FileExists
		}
	}
	if t.FileContains != nil {
		data, err := os.ReadFile(expandHome(t.FileContains.Path))
		if err != nil {
			return false, "missing file " + t.FileContains.Path
		}
		if !strings.Contains(strings.ToLower(string(data)), strings.ToLower(t.FileContains.Text)) {
			return false, fmt.Sprintf("%s doesn't contain %q", t.FileContains.Path, t.FileContains.Text)
		}
	}
	return true, ""
}

func waitFor(state *State, max time.Duration, ok func(Task) bool) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) && !ok(state.LastTask()) {
		time.Sleep(200 * time.Millisecond)
	}
}

// expandHome turns a leading ~ into the home folder.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimLeft(p[1:], `\/`))
		}
	}
	return p
}

var procGetWindowTextW = user32.NewProc("GetWindowTextW")

// foregroundWindowTitle returns the title of the window in front.
func foregroundWindowTitle() string {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return ""
	}
	buf := make([]uint16, 512)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}
