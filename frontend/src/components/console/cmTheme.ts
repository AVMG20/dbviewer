// CodeMirror theme + highlight style built from the app's CSS variables (matches dark and light).
import { EditorView } from "@codemirror/view";
import { HighlightStyle } from "@codemirror/language";
import { tags as t } from "@lezer/highlight";

export const appTheme = EditorView.theme({
  "&": { height: "100%", background: "var(--bg)", color: "var(--tx)", fontSize: "12.5px" },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { fontFamily: "var(--mono)", lineHeight: "1.55", overflow: "auto" },
  ".cm-content": { caretColor: "var(--tx)", padding: "8px 0" },
  ".cm-line": { padding: "0 12px" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--tx)" },
  ".cm-gutters": { background: "var(--bg2)", color: "var(--dim)", border: "none", borderRight: "1px solid var(--bd)" },
  ".cm-activeLine": { background: "color-mix(in srgb, var(--hov) 55%, transparent)" },
  ".cm-activeLineGutter": { background: "var(--hov)", color: "var(--tx)" },
  ".cm-selectionBackground": { background: "var(--selbg) !important" },
  "&.cm-focused .cm-selectionBackground": { background: "var(--selbg) !important" },
  ".cm-selectionMatch": { background: "var(--mark)" },
  ".cm-matchingBracket": { background: "var(--selbg)", outline: "1px solid var(--selbd)" },
  ".cm-placeholder": { color: "var(--dim)" },
  ".cm-tooltip": { zIndex: "var(--z-popover, 40)", background: "var(--bg2)", border: "1px solid var(--bd)", borderRadius: "6px", boxShadow: "0 8px 28px var(--shadow)", color: "var(--tx)" },
  ".cm-tooltip.cm-tooltip-autocomplete > ul": { fontFamily: "var(--mono)", fontSize: "12px", maxHeight: "260px" },
  ".cm-tooltip-autocomplete ul li": { padding: "1px 8px" },
  ".cm-tooltip-autocomplete ul li[aria-selected]": { background: "var(--ac)", color: "var(--ac-fg, #fff)" },
  ".cm-completionDetail": { color: "var(--dim)", fontStyle: "normal", marginLeft: "10px" },
  ".cm-tooltip-autocomplete ul li[aria-selected] .cm-completionDetail": { color: "#fffa" },
  ".cm-panels": { background: "var(--bg2)", color: "var(--tx)", borderColor: "var(--bd)" },
  ".cm-panel input, .cm-panel button": { background: "var(--bg)", border: "1px solid var(--bd)", borderRadius: "4px", color: "var(--tx)" },
  ".cm-searchMatch": { background: "var(--mark)" },
  ".cm-error-line": { background: "color-mix(in srgb, var(--danger) 16%, transparent)" },
});

export const appHighlight = HighlightStyle.define([
  { tag: [t.keyword, t.operatorKeyword, t.modifier], color: "var(--syn-keyword)" },
  { tag: [t.string, t.special(t.string)], color: "var(--syn-string)" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "var(--syn-number)" },
  { tag: [t.lineComment, t.blockComment, t.comment], color: "var(--syn-comment)", fontStyle: "italic" },
  { tag: [t.typeName, t.standard(t.name)], color: "var(--syn-func)" },
  { tag: [t.operator, t.punctuation], color: "var(--dim)" },
  { tag: [t.name, t.variableName], color: "var(--syn-ident)" },
  { tag: [t.propertyName, t.attributeName], color: "var(--syn-func)" },
  { tag: [t.tagName, t.documentMeta, t.processingInstruction], color: "var(--syn-keyword)" },
  { tag: [t.attributeValue], color: "var(--syn-string)" },
  { tag: [t.angleBracket, t.bracket, t.brace, t.squareBracket, t.separator], color: "var(--dim)" },
]);


