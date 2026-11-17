import type { QueryResult } from "@/api";

/** A QueryResult with defaults, for client-side errors and notices. */
export function makeResult(p: Partial<QueryResult> = {}): QueryResult {
  return {
    queryId: "", index: 0, statement: "", columns: [], rows: [], hasResultSet: false, rowsAffected: -1, total: -1,
    offset: 0, hasMore: false, truncated: false, durationMs: 0, error: "", ...p,
  } as QueryResult;
}
