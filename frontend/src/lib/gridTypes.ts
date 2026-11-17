// Shared grid types (data agent).
import type { CellValue, Driver } from "@/api";

export interface GridRow {
  /** Stable key: PK JSON for loaded rows, tempId for new rows, index for console rows. */
  key: string;
  values: CellValue[];
  state: "normal" | "new" | "deleted";
  /** Per-column "has a staged change" flags. */
  changed?: boolean[];
}

export interface SortSpec {
  col: number;
  dir: "asc" | "desc";
}

export interface CellEdit {
  row: number;
  col: number;
  value: CellValue;
}

export interface AppliedEdits {
  edits: CellEdit[];
  /** New rows (sparse arrays indexed by column; undefined = untouched/default). */
  appended: (CellValue | undefined)[][];
}

export interface MenuItem {
  label?: string;
  hint?: string;
  separator?: boolean;
  disabled?: boolean;
  danger?: boolean;
  run?: () => void;
  children?: MenuItem[];
}

export interface SelectionInfo {
  active: { row: number; col: number } | null;
  rowCount: number;
  colCount: number;
  cellCount: number;
}

export interface CopyContext {
  driver: Driver | "";
  schema: string;
  table: string;
}
