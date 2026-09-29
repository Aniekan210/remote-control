/**
 * Mirrors the Go server's Task. I haven't seen the struct's json tags, so
 * normalizeTask() accepts PascalCase (no tags), camelCase or snake_case.
 * Once you know the real shape, you can delete the fallbacks.
 */

export type TaskStatus = "NONE" | "RUNNING" | "PAUSED" | "NEEDS_INPUT" | "COMPLETED";

export type Execution = Record<string, unknown>;

/** blocked = it can't go on as asked · confirm = approve an irreversible step · budget = a per-task cap was reached */
export type QuestionKind = "blocked" | "confirm" | "budget";

export type Task = {
  deviceId: string;
  description: string;
  status: TaskStatus;
  currentInstructionIndex: number;
  instructionList: string[];
  executionList: Execution[];
  /** true while the AI is planning (instruction list not generated yet) */
  planning: boolean;
  /** bumped by the server on every broadcast the worker must act on */
  seq: number;
  /** why the next planner call is a revise, not a fresh plan ("" on the first plan) */
  reason: string;
  /** shown to the user while status is NEEDS_INPUT */
  question: string;
  questionKind: QuestionKind | "";
  /** data: URL of the screen when the question was asked */
  questionImage: string;
  /** every "Q: … / A: …" pair so far */
  answers: string[];
  /** automatic revises so far */
  autoReplans: number;
  /** index of the last step the user approved (-1 = none) */
  confirmedIndex: number;
  /** parallel to instructionList: steps that need approval before they run */
  needsConfirm: boolean[];
  /** what the AI last saw on screen / is doing (shown under the current step) */
  note: string;
  /** OpenRouter spend on this task so far, in USD */
  costUsd: number;
  /** one-off error from the server (budget reached, missing key…); cleared on the next accepted action */
  lastError: string;
};

export const EMPTY_TASK: Task = {
  deviceId: "",
  description: "",
  status: "NONE",
  currentInstructionIndex: 0,
  instructionList: [],
  executionList: [],
  planning: false,
  seq: 0,
  reason: "",
  question: "",
  questionKind: "",
  questionImage: "",
  answers: [],
  autoReplans: 0,
  confirmedIndex: -1,
  needsConfirm: [],
  note: "",
  costUsd: 0,
  lastError: "",
};

type Obj = Record<string, unknown>;

function pick(o: Obj, ...keys: string[]): unknown {
  for (const k of keys) if (o[k] !== undefined && o[k] !== null) return o[k];
  return undefined;
}

const STATUSES: TaskStatus[] = ["NONE", "RUNNING", "PAUSED", "NEEDS_INPUT", "COMPLETED"];
const QUESTION_KINDS: QuestionKind[] = ["blocked", "confirm", "budget"];

export function normalizeTask(raw: unknown): Task {
  if (!raw || typeof raw !== "object") return EMPTY_TASK;
  const o = raw as Obj;
  const status = String(pick(o, "Status", "status") ?? "NONE").toUpperCase() as TaskStatus;
  const instructions = pick(o, "InstructionList", "instructionList", "instruction_list");
  const executions = pick(o, "ExecutionList", "executionList", "execution_list");
  const answers = pick(o, "Answers", "answers");
  const needsConfirm = pick(o, "NeedsConfirm", "needsConfirm", "needs_confirm");
  const questionKind = String(pick(o, "QuestionKind", "questionKind", "question_kind") ?? "");
  return {
    deviceId: String(pick(o, "DeviceID", "deviceId", "device_id") ?? ""),
    description: String(pick(o, "Description", "description") ?? ""),
    status: STATUSES.includes(status) ? status : "NONE",
    currentInstructionIndex: Number(
      pick(o, "CurrentInstructionIndex", "currentInstructionIndex", "current_instruction_index") ?? 0,
    ),
    instructionList: Array.isArray(instructions) ? instructions.map(String) : [],
    executionList: Array.isArray(executions)
      ? executions.map((e) => (e && typeof e === "object" ? (e as Execution) : { value: e }))
      : [],
    planning: Boolean(pick(o, "Context", "context")),
    seq: Number(pick(o, "Seq", "seq") ?? 0) || 0,
    reason: String(pick(o, "Reason", "reason") ?? ""),
    question: String(pick(o, "Question", "question") ?? ""),
    questionKind: QUESTION_KINDS.includes(questionKind as QuestionKind) ? (questionKind as QuestionKind) : "",
    questionImage: String(pick(o, "QuestionImage", "questionImage", "question_image") ?? ""),
    answers: Array.isArray(answers) ? answers.map(String) : [],
    autoReplans: Number(pick(o, "AutoReplans", "autoReplans", "auto_replans") ?? 0) || 0,
    confirmedIndex: Number(pick(o, "ConfirmedIndex", "confirmedIndex", "confirmed_index") ?? -1),
    needsConfirm: Array.isArray(needsConfirm) ? needsConfirm.map(Boolean) : [],
    note: String(pick(o, "Note", "note") ?? ""),
    costUsd: Number(pick(o, "CostUSD", "costUsd", "cost_usd") ?? 0) || 0,
    lastError: String(pick(o, "LastError", "lastError", "last_error") ?? ""),
  };
}

/** Pulls a TASK_UPDATE payload out of a raw WS message, or null. */
export function parseServerMessage(data: string): Task | null {
  try {
    const msg = JSON.parse(data) as Obj;
    const type = pick(msg, "Type", "type");
    if (type !== "TASK_UPDATE") return null;
    return normalizeTask(pick(msg, "Payload", "payload"));
  } catch {
    return null;
  }
}

export type ClientAction =
  | { type: "CREATE_TASK"; description: string }
  | { type: "PAUSE_TASK" }
  | { type: "RESUME_TASK" }
  | { type: "CANCEL_TASK" }
  /** reply to the current question (NEEDS_INPUT); "approve" approves a confirm question */
  | { type: "ANSWER"; description: string };
