/**
 * Generic copy-to-clipboard button, not tied to any one page. The mount
 * root's data-copy-text supplies the text to copy; data-share-url is also
 * read as a fallback so this island already matches the attribute the
 * embed page (a later task) is expected to mount it with, without this file
 * knowing anything about that page's markup. With neither attribute set,
 * the current page URL is copied.
 *
 * bindCopyButton is exported so other islands (the admin one-time API key
 * reveal) can reuse the same clipboard-plus-announcement behaviour instead
 * of duplicating it.
 */

export function mountCopy(root: HTMLElement): void {
  const button = root.querySelector<HTMLButtonElement>('[data-action="copy"]');
  if (!button) return;
  bindCopyButton(button, () => root.dataset.copyText ?? root.dataset.shareUrl ?? window.location.href);
}

/**
 * Wires a single button to copy whatever getText() returns at click time.
 * The button's own label flips to doneLabel briefly (mirroring how sighted
 * users already read a click's result elsewhere in this app), and a
 * visually-hidden status node next to it announces the same result to
 * screen reader users, who would otherwise get no signal at all beyond a
 * label change they cannot see.
 */
export function bindCopyButton(button: HTMLButtonElement, getText: () => string, doneLabel = "Copied!"): void {
  const label = button.textContent ?? "Copy";
  const status = document.createElement("span");
  status.className = "visually-hidden";
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");
  button.after(status);

  button.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(getText());
      button.textContent = doneLabel;
      status.textContent = "Copied to clipboard.";
      window.setTimeout(() => {
        button.textContent = label;
      }, 2000);
    } catch {
      status.textContent = "Could not copy automatically. Select and copy the text manually.";
    }
  });
}
