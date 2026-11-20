// Inline edit sessions for CodeMirror: a target range whose text is temporarily replaced by a
// preview (e.g. SQL streamed by the AI), with an optional block widget above or below it. Generic:
// knows nothing about prompts or Ollama, so other features (fix with AI, explain, …) can reuse it.
//
//   startInline(view, { from, to, dom, side: -1 })   // remember the original text of from..to
//   previewInline(view, text)                         // live preview (not in the undo history)
//   acceptInline(view)                                // keep the preview as ONE undo step
//   discardInline(view)                               // restore the original exactly
//
// Preview changes are dispatched with addToHistory=false and the smallest diff (streams mostly
// append, so the text does not flicker). The range is mapped through other edits. One session
// per editor.
import { StateEffect, StateField, Transaction, type EditorState, type Extension, type Range } from "@codemirror/state";
import { Decoration, EditorView, WidgetType, type DecorationSet } from "@codemirror/view";
import { isolateHistory } from "@codemirror/commands";

export interface InlineTarget {
  /** range to replace; from === to inserts */
  from: number;
  to: number;
  /** widget element (created once by the caller; e.g. a Vue <Teleport> target) */
  dom?: HTMLElement | null;
  /** -1: widget above the first line of the range; 1: below the line the range starts on */
  side?: -1 | 1;
}

export interface InlineState {
  from: number;
  to: number;
  /** text of the range when the session started */
  original: string;
  dom: HTMLElement | null;
  side: -1 | 1;
  /** the range currently holds a preview (differs from the original) */
  previewing: boolean;
  /** preview is still streaming */
  busy: boolean;
}

const setInline = StateEffect.define<InlineState | null>();

class DomWidget extends WidgetType {
  constructor(readonly dom: HTMLElement) {
    super();
  }
  eq(o: WidgetType) {
    return o instanceof DomWidget && o.dom === this.dom;
  }
  toDOM() {
    return this.dom;
  }
  ignoreEvent() {
    return true;
  }
  get estimatedHeight() {
    return this.dom.offsetHeight || 64;
  }
  destroy() {
    /* the caller owns the element */
  }
}

const widgets = new WeakMap<HTMLElement, DomWidget>();
function widgetFor(dom: HTMLElement): DomWidget {
  let w = widgets.get(dom);
  if (!w) widgets.set(dom, (w = new DomWidget(dom)));
  return w;
}

function decorations(state: EditorState, s: InlineState | null): DecorationSet {
  if (!s) return Decoration.none;
  const doc = state.doc;
  const out: Range<Decoration>[] = [];
  const first = doc.lineAt(s.from);
  if (s.dom) {
    const w = widgetFor(s.dom);
    out.push(s.side < 0 ? Decoration.widget({ widget: w, block: true, side: -1 }).range(first.from) : Decoration.widget({ widget: w, block: true, side: 1 }).range(first.to));
  }
  if (s.previewing && s.to > s.from) {
    const cls = s.busy ? "cm-inline-preview cm-inline-busy" : "cm-inline-preview";
    // leading / trailing line breaks (a query inserted after a statement) are not highlighted
    const text = doc.sliceString(s.from, s.to);
    const lead = text.length - text.replace(/^\n+/, "").length;
    const trail = text.length - text.replace(/\n+$/, "").length;
    if (lead < text.length) {
      const startLine = doc.lineAt(s.from + lead).number;
      const endLine = doc.lineAt(s.to - trail).number;
      for (let n = startLine; n <= endLine; n++) out.push(Decoration.line({ class: cls }).range(doc.line(n).from));
    }
  }
  return Decoration.set(out, true);
}

const inlineField = StateField.define<InlineState | null>({
  create: () => null,
  update(value, tr) {
    let v = value;
    if (v && tr.docChanged) {
      // edits at the edges stay inside the range (it is replaced as a whole anyway)
      const from = tr.changes.mapPos(v.from, -1);
      v = { ...v, from, to: Math.max(from, tr.changes.mapPos(v.to, 1)) };
    }
    for (const e of tr.effects) if (e.is(setInline)) v = e.value;
    return v;
  },
});

const inlineDecorations = EditorView.decorations.compute([inlineField], (state) => decorations(state, state.field(inlineField)));

const inlineTheme = EditorView.baseTheme({
  ".cm-inline-preview": { background: "color-mix(in srgb, var(--ok, #3fb950) 11%, transparent)" },
  ".cm-inline-preview.cm-inline-busy": { background: "color-mix(in srgb, var(--ok, #3fb950) 7%, transparent)" },
  ".cm-inline-widget": { padding: "4px 12px 6px", cursor: "default", whiteSpace: "normal" },
});

/** Add to the editor's extensions. */
export function inlineEdit(): Extension {
  return [inlineField, inlineDecorations, inlineTheme];
}

export function getInline(view: EditorView): InlineState | null {
  return view.state.field(inlineField, false) ?? null;
}

/** Starts a session (ends a running one without restoring it). */
export function startInline(view: EditorView, t: InlineTarget): InlineState {
  const from = Math.max(0, Math.min(t.from, view.state.doc.length));
  const to = Math.max(from, Math.min(t.to, view.state.doc.length));
  const s: InlineState = { from, to, original: view.state.sliceDoc(from, to), dom: t.dom ?? null, side: t.side ?? -1, previewing: false, busy: false };
  view.dispatch({ effects: [setInline.of(s), EditorView.scrollIntoView(from, { y: "nearest", yMargin: 80 })] });
  return s;
}

/** Replaces the range's text with `text` (smallest diff, not in the undo history). */
export function previewInline(view: EditorView, text: string, opts: { busy?: boolean } = {}): void {
  const s = getInline(view);
  if (!s) return;
  const cur = view.state.sliceDoc(s.from, s.to);
  const next: InlineState = { ...s, to: s.from + text.length, previewing: text !== s.original, busy: !!opts.busy };
  if (cur === text) {
    if (next.previewing !== s.previewing || next.busy !== s.busy) view.dispatch({ effects: setInline.of(next) });
    return;
  }
  let a = 0;
  while (a < cur.length && a < text.length && cur[a] === text[a]) a++;
  let b = 0;
  while (b < cur.length - a && b < text.length - a && cur[cur.length - 1 - b] === text[text.length - 1 - b]) b++;
  view.dispatch({
    changes: { from: s.from + a, to: s.to - b, insert: text.slice(a, text.length - b) },
    effects: setInline.of(next),
    annotations: [Transaction.addToHistory.of(false)],
  });
}

/** Moves the session to another range: restores the original first, keeps the widget element. */
export function retargetInline(view: EditorView, t: Omit<InlineTarget, "dom">): InlineState | null {
  const s = getInline(view);
  if (!s) return null;
  restore(view, s);
  return startInline(view, { ...t, dom: s.dom });
}

function restore(view: EditorView, s: InlineState) {
  const cur = view.state.sliceDoc(s.from, s.to);
  if (cur !== s.original) {
    view.dispatch({ changes: { from: s.from, to: s.to, insert: s.original }, annotations: [Transaction.addToHistory.of(false)] });
  }
}

/**
 * Keeps the preview (or `text`): the original is restored silently, then the new text is applied
 * as one undo step with the cursor at its end. Ends the session. Returns the final range.
 */
export function acceptInline(view: EditorView, text?: string): { from: number; to: number; text: string } | null {
  const s = getInline(view);
  if (!s) return null;
  const final = text ?? view.state.sliceDoc(s.from, s.to);
  restore(view, s);
  const end = s.from + final.length;
  view.dispatch({
    changes: { from: s.from, to: s.from + s.original.length, insert: final },
    selection: { anchor: end },
    effects: setInline.of(null),
    annotations: [isolateHistory.of("full")],
    userEvent: "input.ai",
    scrollIntoView: true,
  });
  return { from: s.from, to: end, text: final };
}

/** Restores the original text exactly and ends the session. */
export function discardInline(view: EditorView): void {
  const s = getInline(view);
  if (!s) return;
  restore(view, s);
  view.dispatch({ effects: setInline.of(null) });
}

/** Ends the session and leaves the text as it is (e.g. the document was replaced). */
export function endInline(view: EditorView): void {
  if (getInline(view)) view.dispatch({ effects: setInline.of(null) });
}
