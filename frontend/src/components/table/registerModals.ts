// Registers the data-owned modals with the ui store (saveChanges, export, findInDatabase).
// Idempotent; App.vue calls it once after pinia is installed.
import { useUiStore } from "@/stores/ui";
import SaveChangesHost from "./SaveChangesHost.vue";
import ExportModal from "./ExportModal.vue";
import FindInDatabaseModal from "./FindInDatabaseModal.vue";

let done = false;
export function registerDataModals() {
  if (done) return;
  const ui = useUiStore();
  ui.registerModal("saveChanges", SaveChangesHost);
  ui.registerModal("export", ExportModal);
  ui.registerModal("findInDatabase", FindInDatabaseModal);
  done = true;
}
