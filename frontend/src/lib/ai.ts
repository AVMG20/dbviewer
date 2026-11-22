// AI helpers shared by every AI feature:
// - Antigravity CLI (`agy`): writing/changing SQL (Cmd+\), fixing errors, dialect conversion,
//   explaining queries & EXPLAIN plans, plain-language table filters.
// - Local Ollama: fast console tab naming, save-query name suggestions, and inline ghost-text
//   autocomplete while typing (tab naming falls back to Antigravity CLI if Ollama is off).
import type { CancellablePromise } from "@wailsio/runtime";
import {
  aiApi, apiError, newId, onAiChunk,
  type AIChunk, type AICompleteResult, type AIFilterResult, type AIModel, type AISettings, type AISQLRequest, type AISQLResult, type AITaskRequest, type AITextResult,
} from "@/api";
import { DEFAULT_AI_MODEL, settings } from "@/lib/settings";
import { kbd } from "@/lib/kbd";

/** Antigravity CLI AI is enabled (main AI features: generate, fix, explain, convert, filter). */
export function aiReady(): boolean {
  return settings.aiEnabled && !!(settings.aiModel || DEFAULT_AI_MODEL);
}

/** Local Ollama is switched on and a model is chosen (tab naming & inline autocomplete). */
export function ollamaReady(): boolean {
  return settings.ollamaEnabled && !!settings.ollamaModel;
}

/** Tab / query naming is available (prefers local Ollama, falls back to Antigravity CLI). */
export function aiNameReady(): boolean {
  return ollamaReady() || aiReady();
}

/** Inline suggestions (ghost text) in the SQL console are on and local Ollama is ready. */
export function aiInlineReady(): boolean {
  return ollamaReady() && settings.aiInline;
}

/** Shown when an AI shortcut is used while Antigravity CLI AI is off. */
export function aiOffMessage(): string {
  return kbd("AI is off. Turn on Antigravity CLI in Settings (⌘,) → AI.");
}

/** Keep-alive used while an interactive Ollama session is open: never shorter than 2 minutes. */
const SESSION_KEEP_ALIVE = "2m";

/** Antigravity CLI settings sent with main AI calls. */
export function agySettings(): AISettings {
  return {
    provider: "agy",
    baseUrl: "",
    model: settings.aiModel || DEFAULT_AI_MODEL,
    keepAlive: "",
  };
}

/** Local Ollama settings sent with Ollama-backed calls (tab naming & inline autocomplete). */
export function ollamaSettings(session = false): AISettings {
  const keep = settings.ollamaKeepAlive;
  return {
    provider: "ollama",
    baseUrl: settings.ollamaUrl,
    model: settings.ollamaModel,
    keepAlive: session && keep === "0" ? SESSION_KEEP_ALIVE : keep,
  };
}

/** Settings used for NameQuery: local Ollama when configured, otherwise Antigravity CLI. */
function nameSettings(): AISettings {
  return ollamaReady() ? ollamaSettings(false) : agySettings();
}

/** Models available through the Antigravity CLI (`agy models`). */
export async function listAgyModels(): Promise<AIModel[]> {
  return await aiApi.AgyModels();
}

/** Models installed in local Ollama. Rejects (not_connected) when Ollama is not running. */
export async function listOllamaModels(baseUrl = settings.ollamaUrl): Promise<AIModel[]> {
  return await aiApi.Models(baseUrl);
}

/** Picks the default Antigravity CLI model (prefers Hydrogen). */
export function pickDefaultAgyModel(models: AIModel[]): string {
  if (models.some((m) => m.name === DEFAULT_AI_MODEL)) return DEFAULT_AI_MODEL;
  return (
    models.find((m) => /hydrogen/i.test(m.name) || /hydrogen/i.test(m.label ?? ""))?.name ??
    models[0]?.name ??
    DEFAULT_AI_MODEL
  );
}

/** Picks a sensible default Ollama model: prefers instruct / coder models (fast, follow the format). */
export function pickDefaultOllamaModel(models: AIModel[]): string {
  const names = models.map((m) => m.name);
  return names.find((n) => /instruct/i.test(n)) ?? names.find((n) => /coder/i.test(n)) ?? names[0] ?? "";
}

// ---- inline suggestions (local Ollama) -------------------------------------------------
/** The Ollama model for inline suggestions: its own setting, else the Ollama model. */
export function inlineModel(): string {
  return settings.aiInlineModel || settings.ollamaModel;
}

function inlineSettings(): AISettings {
  return { ...ollamaSettings(true), model: inlineModel() };
}

let inlineWarmAt = 0;
let inlineWarmModel = "";

/** Loads the Ollama suggestion model in the background (at most once a minute per model). */
export function warmInline(): void {
  if (!aiInlineReady()) return;
  const m = inlineModel();
  if (m === inlineWarmModel && Date.now() - inlineWarmAt < 60_000) return;
  inlineWarmAt = Date.now();
  inlineWarmModel = m;
  aiApi.Warm(inlineSettings()).catch(() => (inlineWarmAt = 0));
}

/** One inline suggestion at the cursor ("" = none). cancel() stops the model. */
export function completeSql(input: { connId: string; schema: string; before: string; after: string }): AiJob<AICompleteResult> {
  const p = aiApi.CompleteSQL({ ...inlineSettings(), ...input });
  return { promise: p, cancel: () => p.cancel() };
}

export type SqlInput = Omit<AISQLRequest, "requestId" | "provider" | "baseUrl" | "model" | "keepAlive" | "retry" | "context" | "sql"> & {
  sql?: string;
  context?: string;
  retry?: boolean;
};

export interface AiJob<T = AISQLResult> {
  promise: Promise<T>;
  cancel(): void;
}

/** Runs a streaming call: chunks tagged with requestId go to onChunk until it settles or is cancelled. */
function streamJob<T>(call: (requestId: string) => CancellablePromise<T>, onChunk?: (c: AIChunk) => void): AiJob<T> {
  const requestId = newId();
  let done = false;
  const off = onChunk ? onAiChunk((c) => !done && c.requestId === requestId && onChunk(c)) : () => undefined;
  const p = call(requestId);
  const promise = (async () => {
    try {
      return await p;
    } finally {
      done = true;
      off();
    }
  })();
  return {
    promise,
    cancel: () => {
      done = true;
      off();
      p.cancel();
    },
  };
}

/**
 * Writes (input.sql empty) or changes (input.sql set) one SQL statement via Antigravity CLI.
 * onChunk gets the whole SQL so far while the model writes.
 */
export function generateSql(input: SqlInput, onChunk?: (c: AIChunk) => void): AiJob {
  return streamJob(
    (requestId) =>
      aiApi.GenerateSQL({
        ...agySettings(),
        requestId,
        connId: input.connId,
        schema: input.schema,
        prompt: input.prompt,
        sql: input.sql ?? "",
        context: input.context ?? "",
        retry: !!input.retry,
      }),
    onChunk,
  );
}

export type TaskInput = Partial<Omit<AITaskRequest, "requestId" | "provider" | "baseUrl" | "model" | "keepAlive">> & { connId: string };

function taskRequest(input: TaskInput, requestId: string, cfg: AISettings): AITaskRequest {
  return {
    ...cfg,
    requestId,
    connId: input.connId,
    schema: input.schema ?? "",
    sql: input.sql ?? "",
    error: input.error ?? "",
    target: input.target ?? "",
    plan: input.plan ?? "",
    table: input.table ?? "",
    prompt: input.prompt ?? "",
    retry: !!input.retry,
  };
}

type SqlTask = "FixSQL" | "ConvertSQL";
type TextTask = "ExplainSQL" | "ExplainPlan" | "NameQuery";

/** FixSQL / ConvertSQL via Antigravity CLI: streams SQL like generateSql. */
export function sqlTask(task: SqlTask, input: TaskInput, onChunk?: (c: AIChunk) => void): AiJob {
  return streamJob((id) => aiApi[task](taskRequest(input, id, agySettings())), onChunk);
}

/** ExplainSQL / ExplainPlan (Antigravity CLI, streamed) and NameQuery (Ollama preferred, fallback Antigravity CLI). */
export function textTask(task: TextTask, input: TaskInput, onChunk?: (c: AIChunk) => void): AiJob<AITextResult> {
  const cfg = task === "NameQuery" ? nameSettings() : agySettings();
  return streamJob((id) => aiApi[task](taskRequest(input, id, cfg)), onChunk);
}

/** WHERE (+ ORDER BY) for one table from a plain-language request via Antigravity CLI. */
export function filterTask(input: TaskInput): AiJob<AIFilterResult> {
  return streamJob((id) => aiApi.FilterRows(taskRequest(input, id, agySettings())));
}

/** A failed AI call for display: the message and whether the fix is in Settings. */
export function aiError(e: unknown): { message: string; settings: boolean } | null {
  const err = apiError(e);
  if (err.code === "canceled") return null;
  const settingsFix =
    err.code === "not_connected" ||
    err.code === "not_found" ||
    (err.code === "invalid_argument" && /model|ollama url|antigravity/i.test(err.message));
  return { message: err.message || err.code, settings: settingsFix };
}

/** "0.9 s" / "340 ms". */
export function fmtDuration(ms: number): string {
  return ms < 1000 ? `${Math.round(ms)} ms` : `${(ms / 1000).toFixed(1)} s`;
}

// ---- prompt history (localStorage, newest last) -------------------------------------------
const HISTORY_KEY = "dbviewer:aiPrompts";
const HISTORY_MAX = 50;

export function promptHistory(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(HISTORY_KEY) ?? "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

export function rememberPrompt(prompt: string): void {
  const p = prompt.trim();
  if (!p) return;
  try {
    const list = promptHistory().filter((x) => x !== p);
    list.push(p);
    localStorage.setItem(HISTORY_KEY, JSON.stringify(list.slice(-HISTORY_MAX)));
  } catch {
    /* storage unavailable: no history */
  }
}
