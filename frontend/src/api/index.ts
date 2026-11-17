// The ONE place the frontend talks to the Go backend.
// Owned by the backend engineer: frontend agents must not edit this file
// (request changes in NEEDS.md). Generated bindings live in frontend/bindings/
// (regenerate with `wails3 generate bindings -clean=true -ts -i`).
//
// Every call returns a CancellablePromise: `p.cancel()` cancels the Go context
// (useful for RunQuery / FetchPage / CountRows). Rejections are Errors; use
// apiError(e) to get {code, message}.

import { Events, CancellablePromise } from "@wailsio/runtime";
import {
  ConnectionService,
  DataService,
  ProjectService,
  QueryService,
  SchemaService,
} from "../../bindings/dbviewer/internal/services";
import type * as M from "../../bindings/dbviewer/internal/model";
import { ConnStatus, FindProgress as FindProgressEvent, SshHostkey, SshPassphrase } from "../../bindings/events";

// ---------------------------------------------------------------------------
// Type cleanup: the Go side guarantees slices/maps are never null (they are []
// / {}), so the `| null` the generator adds to arrays and maps is stripped here.
// Optional pointer fields (fk, default, password, page) stay nullable.

type IsAny<T> = 0 extends 1 & T ? true : false;
export type Clean<T> =
  IsAny<T> extends true
    ? T
    : T extends (infer U)[]
      ? Clean<U>[]
      : T extends object
        ? { [K in keyof T]: CleanProp<T[K]> }
        : T;
type CleanProp<V> =
  IsAny<V> extends true
    ? V
    : NonNullable<V> extends any[]
      ? Clean<NonNullable<V>>
      : NonNullable<V> extends object
        ? string extends keyof NonNullable<V>
          ? Clean<NonNullable<V>> // map: never null
          : Clean<NonNullable<V>> | Extract<V, null | undefined> // struct pointer: keeps null
        : V;
type CleanFn<F> = F extends (...a: infer A) => CancellablePromise<infer R>
  ? (...a: A) => CancellablePromise<CleanProp<R>>
  : never;
type CleanService<S> = { [K in keyof S]: CleanFn<S[K]> };

// ---------------------------------------------------------------------------
// Model types (camelCase JSON, see CONTRACT.md)

/** Encoded cell value. See CONTRACT.md "Value encoding". */
export type CellValue = null | string | number | boolean | BinaryValue | LongTextValue | DefaultValue;
export interface BinaryValue { $type: "binary"; hex: string; size: number; truncated: boolean }
export interface LongTextValue { $type: "text"; text: string; size: number; truncated: boolean }
/** Only sent TO the backend (ChangeSet): the SQL DEFAULT keyword. */
export interface DefaultValue { $type: "default" }

export type Driver = "postgres" | "mysql" | "sqlite";
export type ConnColor = "none" | "green" | "yellow" | "orange" | "red" | "blue" | "purple";
export type ValueKind =
  | "int" | "float" | "decimal" | "bool" | "string" | "text" | "json"
  | "date" | "time" | "datetime" | "binary" | "uuid" | "other";
export type FilterOp =
  | "eq" | "ne" | "gt" | "gte" | "lt" | "lte" | "contains" | "not_contains" | "starts_with"
  | "ends_with" | "like" | "not_like" | "is_null" | "not_null" | "in" | "not_in" | "between";

export type Project = Clean<M.Project>;
export type ConnectionConfig = Clean<M.ConnectionConfig>;
export type SSHTunnel = Clean<M.SSHTunnel>;
export type ConnectionStatus = Clean<M.ConnectionStatus>;
export type TestResult = Clean<M.TestResult>;
export type EnvImportResult = Clean<M.EnvImportResult>;
export type SchemaInfo = Clean<M.SchemaInfo>;
export type Table = Clean<M.Table>;
export type Column = Clean<M.Column>;
export type FKRef = Clean<M.FKRef>;
export type Index = Clean<M.Index>;
export type ForeignKey = Clean<M.ForeignKey>;
export type Trigger = Clean<M.Trigger>;
export type TableStructure = Clean<M.TableStructure>;
export type SchemaIndex = Clean<M.SchemaIndex>;
export type SchemaIndexTable = Clean<M.SchemaIndexTable>;
export type SearchHit = Clean<M.SearchHit>;
export type ResultColumn = Clean<M.ResultColumn>;
export type QueryResult = Omit<Clean<M.QueryResult>, "rows" | "estimatedTotal"> & {
  rows: CellValue[][];
  /** FetchPage only (always sent by the backend; optional so client-built results stay valid). */
  estimatedTotal?: number;
};
export type TableRef = Clean<M.TableRef>;
export type ColumnFilter = Clean<M.ColumnFilter>;
export type PageRequest = Clean<M.PageRequest>;
export type QueryRequest = Clean<M.QueryRequest>;
export type StatementRange = Clean<M.StatementRange>;
export type CellRequest = Clean<M.CellRequest>;
export type RowUpdate = Clean<M.RowUpdate>;
export type RowInsert = Clean<M.RowInsert>;
export type RowDelete = Clean<M.RowDelete>;
export type ChangeSet = Clean<M.ChangeSet>;
export type ApplyResult = Clean<M.ApplyResult>;
export type StructureChange = Clean<M.StructureChange>;
export type ColumnDef = Clean<M.ColumnDef>;
export type HostKeyPrompt = Clean<M.HostKeyPrompt>;
export type SSHKeyPrompt = Clean<M.SSHKeyPrompt>;
export type SSHAuthAttempt = Clean<M.SSHAuthAttempt>;
export type SSHTestResult = Clean<M.SSHTestResult>;
/** SSHTunnel.authMethod. "auto" is labelled "Default (agent + ~/.ssh)" in the UI; "agent" (old) means the same. */
export type SSHAuthMethod = "auto" | "key" | "password";
export type FindProgress = Clean<M.FindProgress>;
export type ExportRequest = Clean<M.ExportRequest>;
export type ExportResult = Clean<M.ExportResult>;
export type CopySQLRequest = Clean<M.CopySQLRequest>;
export type FindRequest = Clean<M.FindRequest>;
export type FindResult = Omit<Clean<M.FindResult>, "result"> & { result: QueryResult };
export type QueryHistory = Clean<M.QueryHistory>;
export type SavedQuery = Clean<M.SavedQuery>;
export type AppInfo = Clean<M.AppInfo>;

// ---------------------------------------------------------------------------
// Services (1:1 with the Go services; signatures documented in CONTRACT.md)

export const projectApi = ProjectService as unknown as CleanService<typeof ProjectService>;
export const connectionApi = ConnectionService as unknown as CleanService<typeof ConnectionService>;
export const schemaApi = SchemaService as unknown as CleanService<typeof SchemaService>;
export const queryApi = QueryService as unknown as CleanService<typeof QueryService>;
const dataRaw = DataService as unknown as CleanService<typeof DataService>;
export const dataApi = {
  ...dataRaw,
  FetchPage: dataRaw.FetchPage as (req: PageRequest) => CancellablePromise<QueryResult>,
};
export const runQuery = QueryService.RunQuery as unknown as (req: QueryRequest) => CancellablePromise<QueryResult[]>;

/** found["format"] of connectionApi.ParseConnectionString. */
export type ConnectionStringFormat = "url" | "jdbc" | "mysql-dsn" | "libpq" | "env";

/**
 * Parses a pasted connection string (URL, JDBC URL, Go MySQL DSN, libpq key=value,
 * or .env lines) into a prefilled, unsaved ConnectionConfig. Rejects with
 * code "invalid_argument" and a readable message for unrecognized input.
 * Same as connectionApi.ParseConnectionString.
 */
export function parseConnectionString(text: string): CancellablePromise<EnvImportResult> {
  return connectionApi.ParseConnectionString(text);
}

// ---------------------------------------------------------------------------
// Events

/** Subscribe to connection status changes. Returns an unsubscribe function. */
export function onConnStatus(cb: (s: ConnectionStatus) => void): () => void {
  return Events.On(ConnStatus, (ev) => cb(ev.data as ConnectionStatus));
}

/**
 * Unknown SSH host key seen while connecting (the Connect/TestConnection call also
 * rejects with code "host_key_unknown"). Ask the user; on accept call
 * connectionApi.TrustHostKey(p.host, p.fingerprint) and retry.
 */
export function onSshHostKey(cb: (p: HostKeyPrompt) => void): () => void {
  return Events.On(SshHostkey, (ev) => cb(ev.data as HostKeyPrompt));
}

/**
 * An encrypted SSH key file accepted by the server needs its passphrase (the
 * Connect/TestConnection/TestSSH call also rejects with "passphrase_required").
 * Ask the user, call connectionApi.ProvideKeyPassphrase(p.keyPath, passphrase,
 * remember) and retry.
 */
export function onSshPassphrase(cb: (p: SSHKeyPrompt) => void): () => void {
  return Events.On(SshPassphrase, (ev) => cb(ev.data as SSHKeyPrompt));
}

/** Progress of FindInDatabase (one event per searched table). */
export function onFindProgress(cb: (p: FindProgress) => void): () => void {
  return Events.On(FindProgressEvent, (ev) => cb(ev.data as FindProgress));
}

// ---------------------------------------------------------------------------
// Errors

export type ErrorCode =
  | "not_found" | "invalid_argument" | "not_connected" | "read_only" | "no_primary_key"
  | "canceled" | "password_required" | "keychain" /* no longer produced */ | "db_error" | "not_implemented"
  | "internal" | "unsupported" | "ssh_error" | "host_key_unknown" | "host_key_mismatch"
  | "passphrase_required" | "unknown";

export interface ApiError { code: ErrorCode; message: string }

/** Normalizes a rejected backend call into {code, message}. */
export function apiError(e: unknown): ApiError {
  const anyE = e as { cause?: { code?: string; message?: string }; message?: string; name?: string } | undefined;
  if (anyE?.name === "CancelError" || anyE?.name === "CancelledRejectionError") {
    return { code: "canceled", message: "canceled" };
  }
  if (anyE?.cause && typeof anyE.cause === "object" && anyE.cause.code) {
    return { code: anyE.cause.code as ErrorCode, message: anyE.cause.message ?? "" };
  }
  const msg = anyE?.message ?? String(e);
  const m = /^([a-z_]+): ([\s\S]*)$/.exec(msg);
  if (m) return { code: m[1] as ErrorCode, message: m[2] };
  return { code: "unknown", message: msg };
}

/** A per-statement error string from QueryResult.error → {code, message}. */
export function statementError(err: string): ApiError | null {
  if (!err) return null;
  const m = /^(canceled|read_only): ([\s\S]*)$/.exec(err);
  return m ? { code: m[1] as ErrorCode, message: m[2] } : { code: "db_error", message: err };
}

// ---------------------------------------------------------------------------
// Value helpers (shared semantics; the data agent builds formatting in lib/)

export function isBinary(v: CellValue): v is BinaryValue {
  return typeof v === "object" && v !== null && (v as BinaryValue).$type === "binary";
}
export function isLongText(v: CellValue): v is LongTextValue {
  return typeof v === "object" && v !== null && (v as LongTextValue).$type === "text";
}
export const SQL_DEFAULT: DefaultValue = { $type: "default" };

/** Client-generated ids for RunQuery.queryId, FindRequest.searchId, tabs. */
export function newId(): string {
  return crypto.randomUUID();
}
