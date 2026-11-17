// Toast helper that collapses repeats of the same message (e.g. "Connection is read-only" spam).
import { useUiStore } from "@/stores/ui";
import type { ToastKind } from "@/types";

const last = new Map<string, number>();
export function toastOnce(message: string, kind: ToastKind = "info", timeout?: number, windowMs = 2500) {
  const now = Date.now();
  if (now - (last.get(message) ?? 0) < windowMs) return;
  last.set(message, now);
  useUiStore().toast(message, kind, timeout);
}
