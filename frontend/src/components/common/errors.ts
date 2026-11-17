// Readable versions of common driver errors (shell agent), shared by the connection modal and the sidebar.

/** Long, helpful sentence; null when nothing matches. */
export function friendlyError(raw: string): string | null {
  const sock = /dial unix (\S+?):/.exec(raw)?.[1] ?? /(\/\S+\.sock)/.exec(raw)?.[1];
  if (/no such file or directory/i.test(raw) && sock) return `Socket not found: ${sock}. Is the database server running, and is the path right?`;
  if (/access denied|password authentication failed|authentication failed|no pg_hba/i.test(raw)) return "Access denied: check the user name and password.";
  if (/unknown database|database ".*" does not exist|no such database/i.test(raw)) {
    const db = /unknown database '?([^'\s]+)/i.exec(raw)?.[1] ?? /database "([^"]+)"/i.exec(raw)?.[1];
    return `Unknown database${db ? ` "${db}"` : ""}: check the database name.`;
  }
  if (/connection refused/i.test(raw)) {
    const addr = /dial tcp (\S+?):/.exec(raw)?.[1] ?? /(\[?[\w.:-]+\]?:\d+)/.exec(raw)?.[1];
    return `Connection refused${addr ? ` by ${addr}` : ""}: is the server running, and are host and port right?`;
  }
  if (/no such host|lookup .* (failed|no such)/i.test(raw)) return "Host not found: check the host name.";
  if (/timeout|timed out|deadline exceeded/i.test(raw)) return "Timed out connecting: the host did not answer (firewall, wrong host or port, or the server is down).";
  if (/ssl|tls|certificate/i.test(raw)) return "SSL/TLS problem: check the SSL mode (and the server certificate).";
  return null;
}

/** Very short cause for a tree row ("Socket not found"). */
export function shortError(raw: string): string {
  if (/no such file or directory/i.test(raw) && /dial unix|\.sock/.test(raw)) return "Socket not found";
  if (/access denied|password authentication failed|authentication failed|no pg_hba/i.test(raw)) return "Access denied";
  if (/unknown database|database ".*" does not exist|no such database/i.test(raw)) return "Unknown database";
  if (/connection refused/i.test(raw)) return "Connection refused";
  if (/no such host/i.test(raw)) return "Host not found";
  if (/timeout|timed out|deadline exceeded/i.test(raw)) return "Timed out";
  if (/ssl|tls|certificate/i.test(raw)) return "SSL/TLS error";
  const first = raw.split("\n")[0];
  return first.length > 48 ? first.slice(0, 47) + "…" : first;
}
