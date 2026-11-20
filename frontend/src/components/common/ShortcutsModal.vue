<script setup lang="ts">
// Keyboard reference (shell), opened with Cmd+/ or from the palette. Labels go through kbd(),
// so they read "Ctrl+…" off macOS. Keep in sync with the handlers in App.vue, TableView,
// DataGrid, SqlConsole and the sidebar.
import { computed, ref } from "vue";
import { useUiStore } from "@/stores/ui";
import { kbd } from "@/lib/kbd";
import Modal from "./Modal.vue";

const ui = useUiStore();
const q = ref("");

const GROUPS: { title: string; keys: [string, string][] }[] = [
  {
    title: "General",
    keys: [
      ["⌘K", "Jump to table, column, connection or command"],
      ["⌘T", "New SQL console"],
      ["⌘W", "Close tab"],
      ["⌘1 … ⌘9", "Go to tab 1 … 9"],
      ["⌃⇥ / ⌃⇧⇥", "Next / previous tab"],
      ["⌘[ / ⌘]", "Back / forward (also mouse back / forward)"],
      ["⇧⌘F", "Find in database"],
      ["⌘B", "Toggle sidebar"],
      ["⌘E", "Focus sidebar"],
      ["⌘,", "Settings"],
      ["⌘= / ⌘- / ⌘0", "Zoom in / out / reset"],
      ["⌘/", "This list"],
    ],
  },
  {
    title: "Sidebar",
    keys: [
      ["Type", "Filter tables and columns"],
      ["↑ ↓ ← →", "Move, expand, collapse"],
      ["↵", "Open table"],
      ["⌘↵", "Open structure"],
      ["Esc", "Clear the filter"],
    ],
  },
  {
    title: "Table data",
    keys: [
      ["↵ / F2 / type", "Edit cell"],
      ["Tab", "Next cell (while editing)"],
      ["⌘S", "Review and save changes"],
      ["⌘Z / ⇧⌘Z", "Undo / redo a staged change"],
      ["⌘N", "Add row"],
      ["⌘D", "Duplicate row"],
      ["⌘⌫", "Delete selected rows"],
      ["Delete", "Set cell to NULL"],
      ["⌘R / F5", "Refresh"],
      ["⌘F", "Find in loaded rows"],
      ["⌘\\", "Filter rows in plain language with AI (writes the WHERE)"],
      ["Esc (in WHERE)", "Revert the AI filter before applying it"],
      ["⌘-click a key value", "Open the referenced row (foreign key)"],
    ],
  },
  {
    title: "Selection and clipboard",
    keys: [
      ["⇧-click / ⇧ arrows", "Extend selection"],
      ["⌘-click other cells", "Add cells to the selection"],
      ["⌥-click header", "Select column"],
      ["⇧ Space", "Select row"],
      ["⌘A", "Select all"],
      ["⌘C / ⇧⌘C", "Copy / copy with headers"],
      ["⌘V", "Paste rows from a spreadsheet"],
      ["⌘ arrows", "Jump to the edge"],
    ],
  },
  {
    title: "SQL console",
    keys: [
      ["⌘↵", "Run statement at the cursor (or the selection)"],
      ["⌘⇧↵", "Run everything"],
      ["⌘.", "Cancel the running query"],
      ["⌥⇧F", "Format SQL"],
      ["⌘\\", "Write or change SQL with AI (when enabled in Settings)"],
      ["Tab / ⌘↵ / Esc", "AI: accept / accept and run / discard"],
      ["⌥⌘F", "Fix the failed query with AI"],
      ["⌥⌘E", "Explain the statement at the cursor with AI"],
      ["⌃ Space", "Autocomplete"],
      ["Tab / ↵", "Accept the highlighted autocomplete item (the ✨ AI suggestion is the first row)"],
      ["↓ / ↑", "Move between the AI row and the other autocomplete items"],
      ["Tab", "Accept the AI suggestion (gray text) when no autocomplete list is open"],
      ["⌥→", "Accept the next word of the AI suggestion"],
      ["Esc", "Close the autocomplete list, then hide the AI suggestion"],
      ["⌘S", "Save query"],
    ],
  },
];

const groups = computed(() => {
  const n = q.value.trim().toLowerCase();
  return GROUPS.map((g) => ({
    title: g.title,
    keys: g.keys
      .map(([k, d]) => [kbd(k.trim()), d] as [string, string])
      .filter(([k, d]) => !n || d.toLowerCase().includes(n) || k.toLowerCase().includes(n) || g.title.toLowerCase().includes(n)),
  })).filter((g) => g.keys.length);
});
</script>

<template>
  <Modal title="Keyboard shortcuts" :width="720" @close="ui.closeModal()">
    <input v-model="q" data-autofocus class="input mb-3 w-full" placeholder="Filter shortcuts…" spellcheck="false" />
    <div class="cols">
      <section v-for="g in groups" :key="g.title" class="grp">
        <h3>{{ g.title }}</h3>
        <div v-for="[k, d] in g.keys" :key="g.title + k" class="row">
          <span class="desc">{{ d }}</span>
          <kbd class="flex-none">{{ k }}</kbd>
        </div>
      </section>
      <div v-if="!groups.length" class="dim">No shortcut matches "{{ q }}".</div>
    </div>
    <template #footer>
      <button class="btn" @click="ui.closeModal()">Close</button>
    </template>
  </Modal>
</template>

<style scoped>
.cols { columns: 2; column-gap: 24px; max-height: 60vh; overflow: auto; }
.grp { break-inside: avoid; margin-bottom: 14px; }
h3 { margin: 0 0 4px; font-size: 10px; text-transform: uppercase; letter-spacing: .05em; color: var(--dim); font-weight: 600; }
.row { display: flex; align-items: center; gap: 10px; padding: 3px 0; border-bottom: 1px solid var(--row-bd); }
.desc { flex: 1; min-width: 0; }
</style>
