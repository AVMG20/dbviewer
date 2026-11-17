// Frontend-only shared types. Owned by the backend engineer (see CONTRACT.md);
// frontend agents request changes in NEEDS.md.
import type { ColumnFilter, ConnColor, Driver, QueryResult } from "@/api";

export type TabKind = "table" | "console" | "structure";

export interface Tab {
  id: string; // newId()
  kind: TabKind;
  connId: string;
  schema?: string; // "" / undefined = connection default schema
  table?: string; // table + structure tabs
  title: string;
  /** Initial grid state when opened from FK navigation / palette (table tabs). */
  where?: string;
  orderBy?: string;
  /** Initial SQL (console tabs). */
  sql?: string;
  /** Set by the owning view; TabBar shows a badge and close asks for confirmation. */
  dirty?: boolean;
  /** Pinned tabs survive "close others". */
  pinned?: boolean;
  /** Preview tab (italic title): replaced by the next preview open until promoted (tabs.promote). */
  preview?: boolean;
}

/** Persisted (GetUIState("tabs:<projectId>")) shape of the open tabs. */
export interface PersistedTabs {
  tabs: Tab[];
  activeId: string | null;
}

export type Theme = "dark" | "light" | "system";

export type ToastKind = "info" | "success" | "warning" | "error";

export interface Toast {
  id: number;
  message: string;
  kind: ToastKind;
  /** ms; 0 = sticky until dismissed */
  timeout: number;
}

/** Modals the ui store can open (single modal at a time). */
export type ModalName =
  | "connection" // ConnectionModal (create/edit)
  | "password" // ask-password prompt before Connect
  | "saveChanges" // SQL preview + confirm before ApplyChanges
  | "export"
  | "findInDatabase"
  | "confirm"; // generic confirm (ui.confirm())

export interface ModalState {
  name: ModalName;
  /** Modal-specific payload, e.g. { connId } for "connection" edit, { projectId } for create. */
  props: Record<string, unknown>;
}

export interface ConfirmOptions {
  title: string;
  message: string;
  /** Optional SQL shown in a monospace block (e.g. red-connection save). */
  sql?: string;
  confirmText?: string;
  danger?: boolean;
}

/** Grid per-column filter as edited in the popover (maps 1:1 to api ColumnFilter). */
export type GridColumnFilter = ColumnFilter;

/** Connection colors in display order (theme.css has --conn-<color>). */
export const CONN_COLORS: ConnColor[] = ["none", "green", "yellow", "orange", "red", "blue", "purple"];

export const DRIVER_LABEL: Record<Driver, string> = { postgres: "PostgreSQL", mysql: "MySQL", sqlite: "SQLite" };
export const DRIVER_BADGE: Record<Driver, string> = { postgres: "PG", mysql: "MY", sqlite: "SQ" };

/** One result tab in the SQL console. */
export interface ConsoleResult {
  id: string;
  result: QueryResult;
}

/** Copy formats offered by "Copy as". */
export type CopyFormat = "tsv" | "tsv_header" | "csv" | "json" | "sql_insert" | "sql_update" | "markdown" | "in_list";
