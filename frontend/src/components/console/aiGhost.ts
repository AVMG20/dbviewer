// Inline AI suggestions ("ghost text") for CodeMirror: after a pause in typing, asks for a short
// continuation at the cursor and shows it dimmed after the cursor. Generic: the caller supplies
// the request (see SqlConsole: aiApi.CompleteSQL) and decides when it is enabled.
//
//   aiGhost({ enabled: () => …, fetch: ({ before, after }) => ({ promise, cancel }) })
//
// While the autocomplete list is open as well, the suggestion is also shown as the first row of the
// list (the "AI row", rendered into CodeMirror's list DOM, not a completion source: it is not
// filtered, never opens the list on its own, and does not disturb pending native sources). The AI
// row is selected by default; ↓/↑ move between it and the native items (pickField tracks which).
// Keys (bound in SqlEditor, which also owns the native completion keys):
//   Tab           ghostOwnsTab ? acceptGhost : acceptCompletion
//   Enter         acceptGhost when the AI row is selected (aiRowPicked), else the native keymap
//   ↓ ↑ PgDn PgUp ghostListNav (before the completion keymap)
//   Alt-ArrowRight acceptGhostWord
//   Escape        hideGhost (after the completion list is closed)
// Typing the start of the suggestion keeps the rest visible; anything else hides it. Every edit,
// cursor move or blur cancels the pending request; stale responses are dropped.
import { StateEffect, StateField, Transaction, type EditorState, type Extension } from "@codemirror/state";
import { Decoration, EditorView, ViewPlugin, WidgetType, type Command, type DecorationSet, type ViewUpdate } from "@codemirror/view";
import { syntaxTree } from "@codemirror/language";
import { closeCompletion, completionStatus, currentCompletions, selectedCompletionIndex, setSelectedCompletion } from "@codemirror/autocomplete";
import { isolateHistory } from "@codemirror/commands";

export interface GhostRequest {
  /** editor text before / after the cursor (capped) */
  before: string;
  after: string;
}

export interface GhostJob {
  promise: Promise<string>;
  cancel(): void;
}

export interface GhostOptions {
  /** checked before every request (settings, AI ready, read-only, …) */
  enabled: () => boolean;
  fetch: (req: GhostRequest) => GhostJob;
  /** the editor got focus (e.g. load the model) */
  onFocus?: () => void;
  /** no suggestions while this returns true (e.g. an inline AI session is open) */
  blocked?: (view: EditorView) => boolean;
  /** pause after the last keystroke, ms (default 300) */
  debounce?: number;
}

const BEFORE_CHARS = 4000;
const AFTER_CHARS = 1500;

interface Ghost {
  pos: number;
  text: string;
}

const setGhost = StateEffect.define<Ghost | null>();
const setPick = StateEffect.define<boolean>();

/** userEvent of an accepted suggestion */
const ACCEPT_EVENT = "input.ai.ghost";

const ghostField = StateField.define<Ghost | null>({
  create: () => null,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setGhost)) return e.value;
    if (!value) return null;
    if (tr.docChanged) {
      // typing the start of the suggestion consumes it
      let typed: string | null = null;
      let ok = true;
      tr.changes.iterChanges((fromA, toA, _fromB, _toB, inserted) => {
        if (typed !== null || fromA !== value.pos || toA !== fromA) ok = false;
        else typed = inserted.toString();
      });
      if (!ok || !typed || !value.text.startsWith(typed)) return null;
      const pos = value.pos + (typed as string).length;
      const sel = tr.state.selection.main;
      if (!sel.empty || sel.head !== pos) return null;
      const text = value.text.slice((typed as string).length);
      return text ? { pos, text } : null;
    }
    if (tr.selection) {
      const sel = tr.state.selection.main;
      if (!sel.empty || sel.head !== value.pos || tr.state.selection.ranges.length > 1) return null;
    }
    return value;
  },
});

/** The autocomplete list is on screen (also while it is briefly disabled because a source is pending). */
const listField = StateField.define<boolean>({
  create: () => false,
  update(open, tr) {
    if (currentCompletions(tr.state).length) return true;
    return open && completionStatus(tr.state) === "pending";
  },
});

/** The AI row is (or, once a suggestion arrives, will be) the selected row of the open list. Starts
 *  true for every list; moving through the native items without an AI row clears it, so a late
 *  suggestion does not steal the selection. */
const pickField = StateField.define<boolean>({
  create: () => true,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setPick)) value = e.value;
    return tr.state.field(listField) ? value : true;
  },
});

class GhostWidget extends WidgetType {
  constructor(readonly text: string, readonly muted: boolean) {
    super();
  }
  eq(o: GhostWidget) {
    return o.text === this.text && o.muted === this.muted;
  }
  toDOM() {
    const el = document.createElement("span");
    el.className = this.muted ? "cm-ghost cm-ghost-muted" : "cm-ghost";
    el.textContent = this.text;
    el.setAttribute("aria-hidden", "true");
    return el;
  }
  ignoreEvent() {
    return true;
  }
}

const ghostDecorations = EditorView.decorations.compute([ghostField, listField, pickField], (state): DecorationSet => {
  const g = state.field(ghostField);
  if (!g) return Decoration.none;
  const muted = !ghostOwnsTab(state); // a native list item is selected
  return Decoration.set([Decoration.widget({ widget: new GhostWidget(g.text, muted), side: 1 }).range(g.pos)]);
});

// Colors: --ai / --ai-bg (styles/theme.css), with fallbacks for editors outside the app theme.
const ghostTheme = EditorView.baseTheme({
  ".cm-ghost": { opacity: "0.45", fontStyle: "italic", pointerEvents: "none", whiteSpace: "pre" },
  ".cm-ghost.cm-ghost-muted": { opacity: "0.2" },
  ".cm-tooltip-autocomplete ul li.cm-ai-row": {
    display: "flex",
    alignItems: "center",
    gap: "5px",
    color: "inherit",
    whiteSpace: "pre",
    boxShadow: "inset 2px 0 0 var(--ai, #a855f7)",
  },
  ".cm-tooltip-autocomplete ul li.cm-ai-row.cm-ai-row-sel": { background: "var(--ai-bg, #a855f733)" },
  ".cm-ai-row .cm-ai-icon": { flex: "none", color: "var(--ai, #a855f7)" },
  ".cm-ai-row .cm-ai-label": { overflow: "hidden", textOverflow: "ellipsis" },
  ".cm-ai-row .cm-ai-added": { color: "var(--ai, #a855f7)", fontWeight: "600" },
  ".cm-ai-row .cm-ai-detail": {
    marginLeft: "auto",
    paddingLeft: "14px",
    flex: "none",
    color: "var(--dim, #888)",
    fontFamily: "var(--sans, sans-serif)",
    fontSize: "10.5px",
  },
  ".cm-ai-row.cm-ai-row-sel .cm-ai-detail": { color: "var(--ai, #a855f7)" },
  // the native selection stays on item 0 underneath the AI row: do not draw it as selected
  ".cm-tooltip-autocomplete.cm-ai-picked ul li[aria-selected]:not(.cm-ai-row)": { background: "transparent", color: "inherit" },
  ".cm-tooltip-autocomplete.cm-ai-picked ul li[aria-selected]:not(.cm-ai-row) .cm-completionDetail": { color: "var(--dim, #888)" },
});

// ---- AI row in the autocomplete list -------------------------------------------------------------

/** sparkle from components/common/Icon.vue */
const SPARKLE =
  '<svg width="11" height="11" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<path d="M7 2l1.2 3.3L11.5 6.5 8.2 7.7 7 11 5.8 7.7 2.5 6.5l3.3-1.2zM12.5 9.5l.6 1.4 1.4.6-1.4.6-.6 1.4-.6-1.4-1.4-.6 1.4-.6z"/></svg>';
const ROW_CHARS = 48;

/** The row's label: the word before the cursor + the suggestion (whitespace collapsed), truncated. */
function rowLabel(state: EditorState, g: Ghost): { typed: string; added: string } {
  const line = state.doc.lineAt(g.pos);
  let typed = /[\w$]*$/.exec(state.sliceDoc(line.from, g.pos))![0];
  let added = g.text.replace(/\s+/g, " ").replace(/ $/, "");
  if (typed.length > ROW_CHARS - 12) typed = "…" + typed.slice(typed.length - (ROW_CHARS - 13));
  const room = ROW_CHARS - typed.length;
  if (added.length > room) added = added.slice(0, room - 1).replace(/ $/, "") + "…";
  return { typed, added };
}

function completionList(view: EditorView): HTMLElement | null {
  const id = view.contentDOM.getAttribute("aria-controls");
  const ul = id ? view.dom.ownerDocument.getElementById(id) : null;
  return ul?.closest(".cm-tooltip-autocomplete") ? ul : view.dom.querySelector<HTMLElement>(".cm-tooltip-autocomplete > ul");
}

/** Keeps the AI row in CodeMirror's list DOM in sync (the list re-creates its <ul> when the options
 *  change). The row is an <li> without an id: CodeMirror's selection and click handling skip it. Runs
 *  after the tooltip plugin updated the DOM, so there is no frame without the row. */
const aiRowSync = EditorView.updateListener.of((u) => {
  const { state, view } = u;
  const shown = aiRowShown(state);
  if (!shown && !aiRowShown(u.startState)) return;
  const ul = completionList(view);
  if (!ul) return;
  const tip = ul.closest<HTMLElement>(".cm-tooltip-autocomplete");
  let row = ul.querySelector<HTMLElement>(":scope > li.cm-ai-row");
  if (!shown || ul.classList.contains("cm-completionListIncompleteTop")) {
    row?.remove();
    tip?.classList.remove("cm-ai-picked");
    return;
  }
  const g = state.field(ghostField)!;
  const picked = state.field(pickField);
  if (!row) {
    row = document.createElement("li");
    row.className = "cm-ai-row";
    row.setAttribute("role", "option");
    row.title = "AI suggestion: Tab / Enter to accept";
    row.innerHTML = `<span class="cm-ai-icon">${SPARKLE}</span><span class="cm-ai-label"><span class="cm-ai-typed"></span><span class="cm-ai-added"></span></span><span class="cm-ai-detail">AI ⇥</span>`;
    row.addEventListener("mousedown", (e) => {
      e.preventDefault();
      e.stopPropagation();
      acceptGhost(view);
    });
  }
  if (ul.firstChild !== row) ul.insertBefore(row, ul.firstChild);
  const { typed, added } = rowLabel(state, g);
  const [typedEl, addedEl] = row.querySelectorAll<HTMLElement>(".cm-ai-label > span");
  if (typedEl.textContent !== typed) typedEl.textContent = typed;
  if (addedEl.textContent !== added) addedEl.textContent = added;
  const wasPicked = row.classList.contains("cm-ai-row-sel");
  row.classList.toggle("cm-ai-row-sel", picked);
  tip?.classList.toggle("cm-ai-picked", picked);
  if (picked && !wasPicked) ul.scrollTop = 0;
});

export function hasGhost(state: EditorState): boolean {
  return !!state.field(ghostField, false);
}

/** The suggestion is shown as the first row of the open autocomplete list. */
function aiRowShown(state: EditorState): boolean {
  return !!state.field(ghostField, false) && !!state.field(listField, false);
}

/** The AI row is shown and selected: Tab / Enter accept the suggestion. */
export function aiRowPicked(state: EditorState): boolean {
  return aiRowShown(state) && state.field(pickField, false) !== false;
}

/** A suggestion is shown and Tab accepts it: no list is open, or its AI row is selected. */
export function ghostOwnsTab(state: EditorState): boolean {
  if (!state.field(ghostField, false)) return false;
  return !state.field(listField, false) || state.field(pickField, false) !== false;
}

/** ↓ / ↑ / PageDown / PageUp, bound before the completion keymap: moves between the AI row and the
 *  native items; everything else is left to the completion keymap. */
export function ghostListNav(dir: "down" | "up" | "pagedown" | "pageup"): Command {
  return (view) => {
    const state = view.state;
    const n = currentCompletions(state).length;
    if (!n || !state.field(listField, false)) return false; // no (enabled) list: not ours
    const picked = state.field(pickField, false) !== false;
    if (!state.field(ghostField, false)) {
      // native navigation without an AI row: a suggestion arriving later must not take the selection
      if (picked) view.dispatch({ effects: setPick.of(false) });
      return false;
    }
    const sel = selectedCompletionIndex(state) ?? 0;
    if (picked) {
      // the native selection sits on item 0 underneath the AI row
      if (dir === "down") return view.dispatch({ effects: [setPick.of(false), setSelectedCompletion(0)] }), true;
      if (dir === "up") return view.dispatch({ effects: [setPick.of(false), setSelectedCompletion(n - 1)] }), true; // wrap
      if (dir === "pagedown") return view.dispatch({ effects: setPick.of(false) }), false; // the list pages from item 0
      return true; // pageup: already at the top
    }
    if ((dir === "up" || dir === "pageup") && sel === 0) {
      view.dispatch({ effects: setPick.of(true) });
      return true;
    }
    if (dir === "down" && sel === n - 1) {
      view.dispatch({ effects: [setPick.of(true), setSelectedCompletion(0)] }); // wrap
      return true;
    }
    return false;
  };
}

function insertGhost(view: EditorView, text: string): void {
  const g = view.state.field(ghostField, false);
  if (!g || !text) return;
  const rest = g.text.slice(text.length);
  view.dispatch({
    changes: { from: g.pos, insert: text },
    selection: { anchor: g.pos + text.length },
    effects: setGhost.of(rest ? { pos: g.pos + text.length, text: rest } : null),
    annotations: [isolateHistory.of("full"), Transaction.userEvent.of(ACCEPT_EVENT)],
    scrollIntoView: true,
  });
}

/** Inserts the whole suggestion (one undo step). */
export function acceptGhost(view: EditorView): boolean {
  const g = view.state.field(ghostField, false);
  if (!g) return false;
  closeCompletion(view);
  insertGhost(view, g.text);
  return true;
}

/** Inserts the next word of the suggestion (with the whitespace before it). */
export function acceptGhostWord(view: EditorView): boolean {
  const g = view.state.field(ghostField, false);
  if (!g) return false;
  const m = /^\s*(?:[\w$]+|[^\w\s$]+)/.exec(g.text);
  closeCompletion(view);
  insertGhost(view, m ? m[0] : g.text);
  return true;
}

export function hideGhost(view: EditorView): boolean {
  if (!view.state.field(ghostField, false)) return false;
  view.dispatch({ effects: setGhost.of(null) });
  return true;
}

/** Inside a string literal, quoted identifier or comment (incl. unterminated ones). */
function inLiteralOrComment(state: EditorState, pos: number): boolean {
  for (let node: ReturnType<ReturnType<typeof syntaxTree>["resolveInner"]> | null = syntaxTree(state).resolveInner(pos, -1); node; node = node.parent) {
    const name = node.name;
    if (!/String|Comment|QuotedIdentifier/.test(name)) continue;
    if (node.from >= pos) continue;
    if (node.to > pos) return true;
    // ends at the cursor: inside unless it is closed
    const text = state.sliceDoc(node.from, node.to);
    if (name === "LineComment") return true;
    if (name === "BlockComment") return !(text.length >= 4 && text.endsWith("*/"));
    // strings may have a prefix (E'…', X'…'): closed when it ends with its opening quote
    const open = text.search(/['"`]/);
    return open < 0 || text.length - open < 2 || text[text.length - 1] !== text[open];
  }
  return false;
}

/** Where a suggestion may be asked for: the cursor position, or -1. */
function suggestionPos(view: EditorView): number {
  const state = view.state;
  const sel = state.selection;
  if (sel.ranges.length > 1 || !sel.main.empty) return -1;
  const pos = sel.main.head;
  if (!state.doc.toString().trim()) return -1;
  const line = state.doc.lineAt(pos);
  // end of line, or only whitespace / closing brackets / ";" after the cursor
  if (!/^[\s)\]};]*$/.test(state.sliceDoc(pos, line.to))) return -1;
  if (inLiteralOrComment(state, pos)) return -1;
  return pos;
}

/** Transactions that should ask for a new suggestion: typing, deleting, pasting, accepting. */
function isTyping(tr: Transaction): boolean {
  if (!tr.docChanged) return false;
  const ev = tr.annotation(Transaction.userEvent);
  if (!ev) return false;
  if (ev === "input.ai" || ev.startsWith("input.format")) return false; // AI prompt sessions, Format
  return ev.startsWith("input") || ev.startsWith("delete");
}

export function aiGhost(opts: GhostOptions): Extension {
  const delay = opts.debounce ?? 300;
  const plugin = ViewPlugin.fromClass(
    class {
      timer: ReturnType<typeof setTimeout> | undefined;
      job: GhostJob | null = null;
      constructor(readonly view: EditorView) {}

      cancel() {
        clearTimeout(this.timer);
        this.timer = undefined;
        if (this.job) {
          this.job.cancel();
          this.job = null;
        }
      }

      update(u: ViewUpdate) {
        if (u.docChanged || u.selectionSet || u.focusChanged) this.cancel();
        if (u.focusChanged) {
          if (u.view.hasFocus) opts.onFocus?.();
          else if (hasGhost(u.state)) queueMicrotask(() => hideGhost(this.view));
        }
        if (u.transactions.some(isTyping) && !hasGhost(u.state)) this.schedule();
      }

      schedule() {
        if (!opts.enabled()) return;
        this.timer = setTimeout(() => {
          this.timer = undefined;
          this.request();
        }, delay);
      }

      request() {
        const view = this.view;
        if (!opts.enabled() || !view.hasFocus || hasGhost(view.state) || opts.blocked?.(view)) return;
        const pos = suggestionPos(view);
        if (pos < 0) return;
        const doc = view.state.doc;
        const job = opts.fetch({
          before: doc.sliceString(Math.max(0, pos - BEFORE_CHARS), pos),
          after: doc.sliceString(pos, Math.min(doc.length, pos + AFTER_CHARS)),
        });
        this.job = job;
        job.promise.then(
          (text) => {
            if (this.job !== job) return;
            this.job = null;
            const st = view.state;
            // stale: the document or the cursor changed meanwhile
            if (!text || st.doc !== doc || st.selection.main.head !== pos || !st.selection.main.empty || !view.hasFocus) return;
            if (opts.blocked?.(view)) return;
            view.dispatch({ effects: setGhost.of({ pos, text }) });
          },
          () => {
            if (this.job === job) this.job = null;
          },
        );
      }

      destroy() {
        this.cancel();
      }
    },
  );
  return [ghostField, listField, pickField, ghostDecorations, ghostTheme, aiRowSync, plugin];
}
