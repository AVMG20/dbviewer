// Teleported layers (menus, popovers) live under <body>, outside the per-tab `data-accent`.
// This returns the accent of the currently visible tab view so they can carry it on their root.
export function currentAccent(): string | undefined {
  const els = document.querySelectorAll<HTMLElement>("[data-accent]");
  for (const el of els) {
    if (el.offsetParent !== null || el.getClientRects().length) return el.dataset.accent;
  }
  return undefined;
}
