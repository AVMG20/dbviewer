// Clipboard helpers: navigator.clipboard with an execCommand fallback.

export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through to the legacy path */
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.cssText = "position:fixed;top:-1000px;opacity:0";
    document.body.appendChild(ta);
    const prev = document.activeElement as HTMLElement | null;
    ta.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(ta);
    prev?.focus?.();
    return ok;
  } catch {
    return false;
  }
}

/** Reads text from the clipboard; null when access is denied/unavailable. */
export async function readClipboardText(): Promise<string | null> {
  try {
    if (navigator.clipboard?.readText) return await navigator.clipboard.readText();
  } catch {
    /* denied */
  }
  return null;
}

/** Saves text as a file via the browser (export fallback). */
export function downloadText(filename: string, text: string, mime = "text/plain") {
  const url = URL.createObjectURL(new Blob([text], { type: mime + ";charset=utf-8" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
