// User settings (shell). A plain reactive object so pure helpers (renderCell, grids) can read
// it and still re-render when it changes. Loaded and persisted by the ui store (ui:prefs).
import { reactive } from "vue";

export type Density = "compact" | "comfortable";
export type NullStyle = "NULL" | "null" | "∅" | "empty";
export type DateStyle = "iso" | "local";
/** How long Ollama keeps the model loaded after a request ("0" = unload right away). */
export type AIKeepAlive = "0" | "1m" | "5m" | "30m";
export const AI_KEEP_ALIVES: AIKeepAlive[] = ["0", "1m", "5m", "30m"];
export const DEFAULT_AI_MODEL = "gemini-4-hydrogen-medium";
export const DEFAULT_OLLAMA_URL = "http://localhost:11434";

export interface Settings {
  /** UI zoom factor (Ctrl+= / Ctrl+- / Ctrl+0). */
  zoom: number;
  /** Grid row height: compact 22px, comfortable 28px. */
  density: Density;
  /** Rows per page for newly opened tables. */
  pageSize: number;
  /** How NULL cells are shown. */
  nullStyle: NullStyle;
  /** Dates as stored (ISO) or in the local format of the OS. */
  dateStyle: DateStyle;
  /** Antigravity CLI (`agy`) for AI features (write/fix/explain/convert SQL, table filters). Off by default. */
  aiEnabled: boolean;
  /** Antigravity CLI model id (defaults to Hydrogen). */
  aiModel: string;
  /** Local Ollama for fast tab naming and inline ghost-text autocomplete. Off by default. */
  ollamaEnabled: boolean;
  /** Ollama server URL. */
  ollamaUrl: string;
  /** Ollama model name, "" = none chosen. */
  ollamaModel: string;
  ollamaKeepAlive: AIKeepAlive;
  /** Inline suggestions (ghost text) in the SQL console while typing (uses local Ollama). Off by default. */
  aiInline: boolean;
  /** Ollama model for inline suggestions, "" = the Ollama model above. */
  aiInlineModel: string;
}

export const DEFAULT_SETTINGS: Settings = {
  zoom: 1,
  density: "compact",
  pageSize: 500,
  nullStyle: "NULL",
  dateStyle: "iso",
  aiEnabled: false,
  aiModel: DEFAULT_AI_MODEL,
  ollamaEnabled: false,
  ollamaUrl: DEFAULT_OLLAMA_URL,
  ollamaModel: "",
  ollamaKeepAlive: "5m",
  aiInline: false,
  aiInlineModel: "",
};

export const PAGE_SIZES = [100, 250, 500, 1000, 5000];
export const ZOOM_MIN = 0.7;
export const ZOOM_MAX = 1.6;

export const settings = reactive<Settings>({ ...DEFAULT_SETTINGS });

/** Merges persisted values, ignoring anything invalid. */
export function applySettings(p: Partial<Record<keyof Settings, unknown>> | undefined) {
  if (!p) return;
  if (typeof p.zoom === "number" && p.zoom >= ZOOM_MIN && p.zoom <= ZOOM_MAX) settings.zoom = Math.round(p.zoom * 10) / 10;
  if (p.density === "compact" || p.density === "comfortable") settings.density = p.density;
  if (typeof p.pageSize === "number" && PAGE_SIZES.includes(p.pageSize)) settings.pageSize = p.pageSize;
  if (p.nullStyle === "NULL" || p.nullStyle === "null" || p.nullStyle === "∅" || p.nullStyle === "empty") settings.nullStyle = p.nullStyle;
  if (p.dateStyle === "iso" || p.dateStyle === "local") settings.dateStyle = p.dateStyle;

  // Migrate legacy Ollama-only prefs (aiUrl / aiKeepAlive / aiModel without ollamaEnabled).
  const legacy = p as Record<string, unknown>;
  const isLegacyOllama =
    p.ollamaEnabled === undefined &&
    p.ollamaUrl === undefined &&
    p.ollamaModel === undefined &&
    (legacy.aiUrl !== undefined || legacy.aiKeepAlive !== undefined);
  if (isLegacyOllama) {
    if (typeof p.aiEnabled === "boolean") settings.ollamaEnabled = p.aiEnabled;
    if (typeof legacy.aiUrl === "string" && legacy.aiUrl.trim() && legacy.aiUrl.length <= 500) settings.ollamaUrl = legacy.aiUrl.trim();
    if (typeof p.aiModel === "string" && p.aiModel.length <= 200) settings.ollamaModel = p.aiModel.trim();
    if (typeof legacy.aiKeepAlive === "string" && (AI_KEEP_ALIVES as string[]).includes(legacy.aiKeepAlive)) {
      settings.ollamaKeepAlive = legacy.aiKeepAlive as AIKeepAlive;
    }
  } else {
    if (typeof p.aiEnabled === "boolean") settings.aiEnabled = p.aiEnabled;
    if (typeof p.aiModel === "string" && p.aiModel.trim() && p.aiModel.length <= 200) settings.aiModel = p.aiModel.trim();
    if (typeof p.ollamaEnabled === "boolean") settings.ollamaEnabled = p.ollamaEnabled;
    if (typeof p.ollamaUrl === "string" && p.ollamaUrl.trim() && p.ollamaUrl.length <= 500) settings.ollamaUrl = p.ollamaUrl.trim();
    if (typeof p.ollamaModel === "string" && p.ollamaModel.length <= 200) settings.ollamaModel = p.ollamaModel.trim();
    if (typeof p.ollamaKeepAlive === "string" && (AI_KEEP_ALIVES as string[]).includes(p.ollamaKeepAlive)) {
      settings.ollamaKeepAlive = p.ollamaKeepAlive as AIKeepAlive;
    }
  }
  if (typeof p.aiInline === "boolean") settings.aiInline = p.aiInline;
  if (typeof p.aiInlineModel === "string" && p.aiInlineModel.length <= 200) settings.aiInlineModel = p.aiInlineModel.trim();
}

/** Grid row height in px for the current density. */
export function rowHeight(): number {
  return settings.density === "comfortable" ? 28 : 22;
}

/** Text a NULL cell shows. */
export function nullText(): string {
  return settings.nullStyle === "empty" ? "" : settings.nullStyle;
}
