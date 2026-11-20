// Local AI (Ollama) helpers shared by every AI feature: settings → request payload, model
// list, warm/release of the model, streaming SQL generation, error texts and the prompt history.
//
//   if (!aiReady()) return aiOffToast(ui);
//   const job = generateSql({ connId, schema, prompt, sql, context }, (c) => preview(c.sql), { session: true });
//   const res = await job.promise;   // rejects with {code: "canceled"} after job.cancel()
//
// Sessions: an interactive feature (the inline prompt) keeps the model loaded while it is open
// (warm() + generateSql(..., {session: true})) and calls release() when it closes; release()
// unloads the model only when the user chose "Unload right away" (aiKeepAlive "0").
import type { CancellablePromise } from "@wailsio/runtime";
import {
  aiApi, apiError, newId, onAiChunk,
  type AIChunk, type AICompleteResult, type AIFilterResult, type AIModel, type AISettings, type AISQLRequest, type AISQLResult, type AITaskRequest, type AITextResult, type ApiError,
} from "@/api";
import { settings } from "@/lib/settings";
import { kbd } from "@/lib/kbd";

/** AI is switched on and a model is chosen. */
export function aiReady(): boolean {
  return settings.aiEnabled && !!settings.aiModel;
}

/** Inline suggestions (ghost text) in the SQL console are on and AI is ready. */
export function aiInlineReady(): boolean {
  return aiReady() && settings.aiInline;
}

/** Shown when an AI shortcut is used while AI is off. */
export function aiOffMessage(): string {
  return settings.aiEnabled
    ? kbd("Choose an AI model in Settings (⌘,) first.")
    : kbd("AI is off. Turn it on in Settings (⌘,) → AI.");
}

/** Keep-alive used while an interactive session is open: never shorter than 2 minutes. */
const SESSION_KEEP_ALIVE = "2m";

/** The Ollama settings sent with every call. */
export function aiSettings(session = false): AISettings {
  const keep = settings.aiKeepAlive;
  return {
    baseUrl: settings.aiUrl,
    model: settings.aiModel,
    keepAlive: session && keep === "0" ? SESSION_KEEP_ALIVE : keep,
  };
}

/** Installed models, sorted by name. Rejects (not_connected) when Ollama is not running. */
export async function listModels(baseUrl = settings.aiUrl): Promise<AIModel[]> {
  return await aiApi.Models(baseUrl);
}

/** Picks a sensible default model: prefers instruct / coder models (fast, follow the format). */
export function pickDefaultModel(models: AIModel[]): string {
  const names = models.map((m) => m.name);
  return names.find((n) => /instruct/i.test(n)) ?? names.find((n) => /coder/i.test(n)) ?? names[0] ?? "";
}

/** Loads the model in the background. Resolves with the error (or null), never rejects. */
export async function warm(): Promise<ApiError | null> {
  if (!aiReady()) return null;
  try {
    await aiApi.Warm(aiSettings(true));
    return null;
  } catch (e) {
    return apiError(e);
  }
}

/** Ends an interactive session: unloads the model when the user wants it unloaded right away. */
export function release(): void {
  if (settings.aiModel && settings.aiKeepAlive === "0") aiApi.Unload(aiSettings()).catch(() => undefined);
}

// ---- inline suggestions ----------------------------------------------------------------
/** The model for inline suggestions: its own setting, else the main model. */
export function inlineModel(): string {
  return settings.aiInlineModel || settings.aiModel;
}

function inlineSettings(): AISettings {
  return { ...aiSettings(true), model: inlineModel() };
}

let inlineWarmAt = 0;
let inlineWarmModel = "";

/** Loads the suggestion model in the background (at most once a minute per model). */
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

export type SqlInput = Omit<AISQLRequest, "requestId" | "baseUrl" | "model" | "keepAlive" | "retry" | "context" | "sql"> & {
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
 * Writes (input.sql empty) or changes (input.sql set) one SQL statement. onChunk gets the whole
 * SQL so far while the model writes (chunk.thinking: still reasoning, no SQL yet).
 */
export function generateSql(input: SqlInput, onChunk?: (c: AIChunk) => void, opts: { session?: boolean } = {}): AiJob {
  return streamJob(
    (requestId) =>
      aiApi.GenerateSQL({
        ...aiSettings(!!opts.session),
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

export type TaskInput = Partial<Omit<AITaskRequest, "requestId" | "baseUrl" | "model" | "keepAlive">> & { connId: string };

function taskRequest(input: TaskInput, requestId: string, session: boolean): AITaskRequest {
  return {
    ...aiSettings(session),
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

/** FixSQL / ConvertSQL: streams SQL like generateSql. */
export function sqlTask(task: SqlTask, input: TaskInput, onChunk?: (c: AIChunk) => void, opts: { session?: boolean } = {}): AiJob {
  return streamJob((id) => aiApi[task](taskRequest(input, id, !!opts.session)), onChunk);
}

/** ExplainSQL / ExplainPlan (streamed: chunk.text) and NameQuery (not streamed). */
export function textTask(task: TextTask, input: TaskInput, onChunk?: (c: AIChunk) => void, opts: { session?: boolean } = {}): AiJob<AITextResult> {
  return streamJob((id) => aiApi[task](taskRequest(input, id, !!opts.session)), onChunk);
}

/** WHERE (+ ORDER BY) for one table from a plain-language request. */
export function filterTask(input: TaskInput, opts: { session?: boolean } = {}): AiJob<AIFilterResult> {
  return streamJob((id) => aiApi.FilterRows(taskRequest(input, id, !!opts.session)));
}

/** A failed AI call for display: the message and whether the fix is in Settings. */
export function aiError(e: unknown): { message: string; settings: boolean } | null {
  const err = apiError(e);
  if (err.code === "canceled") return null;
  const settingsFix = err.code === "not_connected" || err.code === "not_found" || (err.code === "invalid_argument" && /model|ollama url/i.test(err.message));
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
