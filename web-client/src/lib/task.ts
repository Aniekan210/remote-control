/**
 * Mirrors the Go server's Task. I haven't seen the struct's json tags, so
 * normalizeTask() accepts PascalCase (no tags), camelCase or snake_case.
 * Once you know the real shape, you can delete the fallbacks.
 */

export type TaskStatus = "NONE" | "RUNNING" | "PAUSED" | "COMPLETED";

export type Execution = Record<string, unknown>;

export type Task = {
  deviceId: string;
  description: string;
  status: TaskStatus;
  currentInstructionIndex: number;
  instructionList: string[];
  executionList: Execution[];
  /** true while the AI is planning (instruction list not generated yet) */
  planning: boolean;
};

export const EMPTY_TASK: Task = {
  deviceId: "",
  description: "",
  status: "NONE",
  currentInstructionIndex: 0,
  instructionList: [],
  executionList: [],
  planning: false,
};

type Obj = Record<string, unknown>;

function pick(o: Obj, ...keys: string[]): unknown {
  for (const k of keys) if (o[k] !== undefined && o[k] !== null) return o[k];
  return undefined;
}

const STATUSES: TaskStatus[] = ["NONE", "RUNNING", "PAUSED", "COMPLETED"];

export function normalizeTask(raw: unknown): Task {
  if (!raw || typeof raw !== "object") return EMPTY_TASK;
  const o = raw as Obj;
  const status = String(pick(o, "Status", "status") ?? "NONE").toUpperCase() as TaskStatus;
  const instructions = pick(o, "InstructionList", "instructionList", "instruction_list");
  const executions = pick(o, "ExecutionList", "executionList", "execution_list");
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
  | { type: "CANCEL_TASK" };
