// Platform-aware shortcut labels. Write hints the macOS way ("⌘S", "⇧⌘F", "⌘↵", "⌘-click");
// kbd() returns them unchanged on macOS and as "Ctrl+S", "Ctrl+Shift+F", "Ctrl+Enter",
// "Ctrl-click" elsewhere. Works on whole sentences too ("Press ⌘N to add a row.").
import { isMac } from "@/components/common/keys";

const KEY_NAMES: Record<string, string> = { "↵": "Enter", "⌫": "Backspace", "⎋": "Esc", "⇥": "Tab" };

/** One shortcut chord or glyph run: modifiers, then an optional key or "-click". */
const CHORD = /[⌘⇧⌥⌃]+(?:-click|[↵⌫⎋⇥]|[A-Za-z0-9.,/[\]=+-](?![A-Za-z]))?/g;

function convertChord(chord: string): string {
  const click = chord.endsWith("-click");
  const body = click ? chord.slice(0, -6) : chord;
  const mods: string[] = [];
  if (body.includes("⌘") || body.includes("⌃")) mods.push("Ctrl");
  if (body.includes("⌥")) mods.push("Alt");
  if (body.includes("⇧")) mods.push("Shift");
  const key = body.replace(/[⌘⇧⌥⌃]/g, "");
  if (click) return mods.join("+") + "-click";
  if (!key) return mods.join("+");
  return [...mods, KEY_NAMES[key] ?? key.toUpperCase()].join("+");
}

export function kbd(s: string, mac = isMac): string {
  if (mac) return s;
  return s.replace(CHORD, convertChord);
}

/** Modifier name for prose: "⌘" on macOS, "Ctrl" elsewhere. */
export const modKey = isMac ? "⌘" : "Ctrl";
