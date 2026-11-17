# Brief: style exploration artifact (dark-first, plus light)

Build a clickable style study for the DB Viewer app and publish it with the `mcp__artifacts__create_artifact` tool, which hosts it on artifacts.avmg.nl. Load the artifact tools with ToolSearch: "select:mcp__artifacts__get_guide,mcp__artifacts__create_artifact,mcp__artifacts__get_artifact,mcp__artifacts__edit_artifact". Call get_guide once first and follow its sandbox rules (no localStorage; use Kit.store if you need state). Do not write files into /Users/arno/tools/dbviewer, except that you may read files there.

## Goal
The user likes the app's LAYOUT and wants it kept exactly. Only the visual styling should change: cleaner and more modern, mainly in dark mode, with light mode as well.

- **Main reference: T3 Code.** Its look:
  - near-black canvas (around #0b0b0c to #111) with slightly lifted panels (#141416 / #18181b)
  - hairline borders (white at 6–8% alpha)
  - soft grey secondary text (#8b8b93 range), crisp white primary text
  - medium-rounded controls (8–10px radius) and pill-shaped chips and badges
  - a restrained blue accent (#3b82f6-ish) with a soft glow and focus ring
  - generous but not wasteful spacing, the Inter/system UI font for chrome, and very few lines and boxes
- **It must stay a useful data tool.** The grid needs enough contrast: row separators that are visible but subtle, clear NULL vs empty, readable monospace data, a clear selected cell and row, visible staged-change colors (amber, green, red), and good contrast for headers and type labels.

## What to show (use the real app layout)
To see the current app, look at the screenshots:
- `/private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/shots/smoke.png`
- `/private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/d2.png` (structure view)
- `/private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/d3.png` (command palette)

Read them with the Read tool. Also read the current theme tokens in `/Users/arno/tools/dbviewer/frontend/src/styles/theme.css` and `main.css`, so your proposal maps onto the same token names.

The layout to recreate:
- a 38px title bar with macOS traffic lights, the sidebar toggle, a centered title, and "Jump to ⌘K"
- the left sidebar: a project dropdown plus a "+ Connection" button, and a tree of connection → database → Tables (count) → table rows. The active table is highlighted, and connections show a color dot and a driver badge.
- the tab bar, with a connection-colour top stripe on the active tab, an italic preview tab, and a lock icon on a read-only tab
- the table toolbar: refresh, "1–500 of 2,000", prev/next, a rows dropdown, add/duplicate/delete, Revert, Save (accent, with a pending badge), "Copy as", Export, and the record-panel toggle
- WHERE and ORDER BY inputs
- the data grid with row numbers, headers (name plus dim type, PK/FK icons, sort arrow), NULLs, numbers right-aligned, a selected cell, a selected row range, one amber edited cell, one green new row, and one red struck-through deleted row
- the status bar

Also include, togglable:
- the ⌘K palette overlay
- a confirm modal with an SQL preview
- the connection modal (driver segmented control, fields, colour swatches, read-only switch)
- a context menu

## Variants
Offer three style variants, switchable by a segmented control at the top of the page (outside the app frame):
1. **"T3"**: as close to T3 Code's aesthetic as possible.
2. **"T3 + contrast"**: the same, with higher contrast in the grid and the panels, for long data sessions.
3. **"Graphite"**: your own take, modern and calm, slightly warmer greys.

Each variant also needs a light mode, with a dark/light toggle next to the variant switch. Build each variant as one set of CSS variables, so the winner can be copied straight into theme.css. Print the token set of the selected variant in a collapsible `<pre>` below the frame, ready to copy.

Connection accent colors must still work: green, red, yellow and blue connections recolor the accent. Add a small switch to change the "current connection colour".

## Constraints
- Use Vue 3 from a pinned CDN (https://unpkg.com/vue@3.4.38/dist/vue.global.prod.js) or plain JS, whichever you prefer.
- Use fake data (Magento-like sales_order rows).
- Make the frame fill the viewport like a desktop window.
- After publishing, call get_artifact and fix any `page_errors`.

## Reply
Reply with the artifact URL and three bullets, one per variant, saying what makes it different.
