/**
 * Mobile folder picker: a bottom sheet listing the folder tree, for
 * filtering the browse grid on a phone. The Folders/Tags/Uploaders rail is
 * hidden below 720px (see main.css) because it assumes the 1440px desktop
 * layout; this sheet is its replacement for folders specifically, opened
 * from a toolbar button that only appears at that width.
 *
 * Rows are plain links built server-side (see browse.html), the same
 * queryString-based navigation the desktop rail uses; there is no folder
 * create, rename or move route yet, so this is filtering only, exactly like
 * the rail it replaces. A row click is a normal navigation, which already
 * closes the sheet by leaving the page; the JS here only has to handle
 * opening, and closing via Escape or the backdrop without navigating.
 *
 * Modal semantics mirror lightbox.ts: role="dialog", aria-modal, a focus
 * trap while open, Escape to close, and focus restored to whatever opened
 * it, so a sheet that must be reachable and dismissable by keyboard users
 * behaves the same way the item viewer already does.
 */
export function mountFolderSheet(root: HTMLElement): void {
  const panel = root.querySelector<HTMLElement>(".folder-sheet__panel");
  const trigger = document.querySelector<HTMLButtonElement>('[data-action="toggle-folders"]');
  if (!panel) return;

  // The element to return focus to on close: normally the trigger button,
  // but tracked explicitly in case focus moved elsewhere before Escape.
  let opener: HTMLElement | null = null;

  function focusableElements(): HTMLElement[] {
    return Array.from(
      panel!.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])'),
    ).filter((el) => !el.hasAttribute("hidden"));
  }

  function open(): void {
    opener = document.activeElement instanceof HTMLElement ? document.activeElement : trigger;
    root.hidden = false;
    trigger?.setAttribute("aria-expanded", "true");
    // Focus the close button first, the same initial-focus choice
    // lightbox.ts makes, rather than the first row: landing on a row would
    // read as "this is already selected" to a screen reader.
    const closeButton = panel!.querySelector<HTMLButtonElement>('[data-action="close"]');
    closeButton?.focus();
  }

  function close(): void {
    root.hidden = true;
    trigger?.setAttribute("aria-expanded", "false");
    if (opener && document.contains(opener)) opener.focus();
    opener = null;
  }

  trigger?.addEventListener("click", () => {
    if (root.hidden) open();
    else close();
  });

  for (const button of root.querySelectorAll('[data-action="close"]')) {
    button.addEventListener("click", () => close());
  }

  document.addEventListener("keydown", (event) => {
    if (root.hidden) return;
    if (event.key === "Escape") {
      close();
      return;
    }
    // Trap Tab within the panel while the sheet is open, so it never leaks
    // keyboard focus into the grid behind it.
    if (event.key === "Tab") {
      const focusable = focusableElements();
      if (focusable.length === 0) return;
      const first = focusable[0]!;
      const last = focusable[focusable.length - 1]!;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }
  });
}
