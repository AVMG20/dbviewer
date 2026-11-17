// Platform-aware shortcut helpers (shell agent). Use these instead of `metaKey || ctrlKey`:
// on macOS Ctrl+K/W/T/F/A are text-editing keys, so only Cmd counts there.
export const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/i.test(navigator.platform || navigator.userAgent);

/** True when the platform's command modifier is held (Cmd on macOS, Ctrl elsewhere). */
export function isMod(e: KeyboardEvent | MouseEvent): boolean {
  return isMac ? e.metaKey : e.ctrlKey;
}
