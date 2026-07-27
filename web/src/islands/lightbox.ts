/**
 * Item viewer: media, inline rename, tag editing and delete. Opening pushes
 * ?item=<id> so a view is linkable and the back button closes it.
 */

import type { ApiItem } from "./grid";
import { stopActivePreview } from "./preview";
import { formatBytes, formatDuration } from "../format";
import { apiSend } from "../request";

/**
 * Maps a numeric uploader id to its username. The /api/items item shape only
 * ever carries the numeric `uploader` id (that is the committed API
 * contract; see grid.ts's ApiItem), so "by <name>" here reads a small JSON
 * directory the page embeds alongside itself instead of the contract
 * growing a display-only field. Missing (e.g. the script tag is absent, or
 * this build point is reused somewhere that never renders it) degrades to
 * showing nothing rather than throwing.
 */
function uploaderDirectory(): Record<string, string> {
  const el = document.getElementById("uploader-directory");
  if (!el || !el.textContent) return {};
  try {
    return JSON.parse(el.textContent) as Record<string, string>;
  } catch {
    return {};
  }
}

export function mountLightbox(root: HTMLElement): void {
  const idLabel = root.querySelector<HTMLElement>('[data-role="id"]');
  const mediaBox = root.querySelector<HTMLElement>('[data-role="media"]');
  const titleInput = root.querySelector<HTMLInputElement>('[data-role="title"]');
  const metaList = root.querySelector<HTMLElement>('[data-role="meta"]');
  const tagList = root.querySelector<HTMLElement>('[data-role="tags"]');
  const tagForm = root.querySelector<HTMLFormElement>('[data-role="tagform"]');
  const shareLink = root.querySelector<HTMLAnchorElement>('[data-role="share"]');
  const sourceLink = root.querySelector<HTMLAnchorElement>('[data-role="source"]');
  const downloadLink = root.querySelector<HTMLAnchorElement>('[data-role="download"]');
  const errorBox = root.querySelector<HTMLElement>('[data-role="error"]');
  // Rendered server-side from the same folder tree the rail uses (see
  // browse.html), so there is no second request for a list the page already
  // carries, and the options match the rail exactly.
  const folderSelect = root.querySelector<HTMLSelectElement>('[data-role="folder"]');
  if (!mediaBox || !titleInput || !tagList || !tagForm || !shareLink || !sourceLink || !errorBox) return;

  const directory = uploaderDirectory();
  let current: ApiItem | null = null;
  // The element that opened the lightbox (a tile's "open" button), so focus
  // can return to it on close. Stays null for opens with no user gesture to
  // return to (a deep link, or the back/forward button).
  let trigger: HTMLElement | null = null;

  /** Interactive elements within the panel, in document (tab) order. */
  function focusableElements(): HTMLElement[] {
    const panel = root.querySelector<HTMLElement>(".lightbox__panel");
    if (!panel) return [];
    return Array.from(
      panel.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ),
    ).filter((el) => !el.hasAttribute("hidden"));
  }

  function showError(message: string): void {
    errorBox!.textContent = message;
    errorBox!.removeAttribute("hidden");
  }

  function clearError(): void {
    errorBox!.textContent = "";
    errorBox!.setAttribute("hidden", "");
  }

  /** Who may delete this item, for the 403 message: a legitimate response
   * for a non-uploader, non-admin, not a bug to hide. */
  function uploaderName(item: ApiItem): string {
    return directory[String(item.uploader)] ?? "the uploader";
  }

  async function open(id: string, pushState = true, openedFrom: HTMLElement | null = null): Promise<void> {
    // A grid tile's hover preview can still be playing (or about to start)
    // when its own click opens the lightbox over it; without this it would
    // keep decoding frames, invisible, behind the modal until the pointer
    // happens to move again.
    stopActivePreview();
    clearError();
    const result = await apiSend<{ item: ApiItem }>(
      `/api/items/${encodeURIComponent(id)}`,
      "GET",
      undefined,
      "That item could not be loaded.",
    );
    if (!result.ok) {
      showError(result.message);
      return;
    }
    const { item } = result.body;
    current = item;

    if (idLabel) idLabel.textContent = item.id;
    mediaBox!.replaceChildren(renderMedia(item));
    titleInput!.value = item.title;
    renderMeta(item);
    renderTags(item.tags);
    if (folderSelect) folderSelect.value = String(item.folder_id ?? 0);
    shareLink!.href = item.share_url;
    if (downloadLink) {
      downloadLink.href = item.media_url;
      downloadLink.download = item.ext ? `${item.title}.${item.ext}` : item.title;
    }
    if (item.source_url) {
      sourceLink!.href = item.source_url;
      sourceLink!.removeAttribute("hidden");
    } else {
      sourceLink!.setAttribute("hidden", "");
    }
    trigger = openedFrom ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
    root.removeAttribute("hidden");
    // Move keyboard focus into the dialog so it does not stay behind on the
    // element that opened it: a screen-reader or keyboard user otherwise
    // gets no signal that a modal opened at all.
    const initialFocus = root.querySelector<HTMLButtonElement>('button[data-action="close"]');
    initialFocus?.focus();
    if (pushState) {
      const url = new URL(window.location.href);
      url.searchParams.set("item", item.id);
      window.history.pushState({ item: item.id }, "", url);
    }
  }

  function close(pushState = true): void {
    root.setAttribute("hidden", "");
    mediaBox!.replaceChildren(); // stop any playing video
    current = null;
    if (pushState) {
      const url = new URL(window.location.href);
      url.searchParams.delete("item");
      window.history.pushState({}, "", url);
    }
    // Restore focus to whatever opened the dialog, so a keyboard user lands
    // back where they were instead of at the top of the document.
    if (trigger && document.contains(trigger)) trigger.focus();
    trigger = null;
  }

  function renderMedia(item: ApiItem): HTMLElement {
    if (item.is_video) {
      const video = document.createElement("video");
      video.src = item.media_url;
      video.controls = true;
      video.preload = "metadata";
      video.playsInline = true;
      // The public media route is rate-limited per client (see
      // handleRawMedia/checkPublicRateLimit); a 429 surfaces here as a
      // generic media "error" event, not a fetch response this code sees
      // directly, so the message is necessarily non-specific about timing.
      video.addEventListener("error", () => {
        showError("Too many streams at once. Playback resumes shortly.");
      });
      return video;
    }
    const img = document.createElement("img");
    img.src = item.media_url;
    img.alt = item.title;
    img.addEventListener("error", () => {
      showError("Too many streams at once. Playback resumes shortly.");
    });
    return img;
  }

  function renderMeta(item: ApiItem): void {
    if (!metaList) return;
    metaList.replaceChildren();
    const rows: Array<[string, string]> = [
      ["size", formatBytes(item.size)],
      ["dims", item.is_video
        ? `${formatDuration(item.duration)}${item.width ? ` · ${item.width}×${item.height}` : ""}`
        : item.width ? `${item.width}×${item.height}` : "unknown"],
      ["mime", item.mime],
      ["by", uploaderName(item)],
    ];
    for (const [key, value] of rows) {
      const dt = document.createElement("dt");
      dt.textContent = key;
      const dd = document.createElement("dd");
      dd.textContent = value;
      metaList.append(dt, dd);
    }
  }

  function renderTags(tags: string[]): void {
    tagList!.replaceChildren();
    for (const tag of tags) {
      const chip = document.createElement("button");
      chip.type = "button";
      chip.className = "chip";
      chip.textContent = `${tag} ×`;
      chip.addEventListener("click", () => void removeTag(tag));
      tagList!.appendChild(chip);
    }
  }

  /**
   * The chip list is only ever re-rendered from a successful response, so a
   * failure here leaves the tags exactly as they were on screen; there is no
   * optimistic state to roll back, only an error to make sure gets shown.
   */
  async function removeTag(tag: string): Promise<void> {
    if (!current) return;
    const result = await apiSend<{ tags: string[] }>(
      `/api/items/${encodeURIComponent(current.id)}/tags/${encodeURIComponent(tag)}`,
      "DELETE",
      undefined,
      "That tag could not be removed.",
    );
    if (!result.ok) {
      showError(result.message);
      return;
    }
    renderTags(result.body.tags);
  }

  tagForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!current) return;
    const field = tagForm.querySelector<HTMLInputElement>('input[name="tag"]');
    const value = field?.value.trim();
    if (!value) return;
    // The field is cleared only on success, so a rejected tag stays typed and
    // can be corrected rather than retyped.
    const result = await apiSend<{ tags: string[] }>(
      `/api/items/${encodeURIComponent(current.id)}/tags`,
      "POST",
      { tag: value },
      "That tag was not accepted.",
    );
    if (!result.ok) {
      showError(result.message);
      return;
    }
    renderTags(result.body.tags);
    if (field) field.value = "";
    clearError();
  });

  // Commit a rename on blur or Enter.
  //
  // A failure deliberately does NOT restore the old title into the field.
  // The text there is what the user typed, and throwing it away to "revert"
  // would destroy their work; the error says plainly that it did not save,
  // and leaving the edit in place is what lets them try again.
  async function saveTitle(): Promise<void> {
    if (!current || titleInput!.value === current.title) return;
    const result = await apiSend<{ item: ApiItem }>(
      `/api/items/${encodeURIComponent(current.id)}`,
      "PATCH",
      { title: titleInput!.value },
      "That rename did not save.",
    );
    if (!result.ok) {
      showError(result.message);
      return;
    }
    const { item } = result.body;
    current = item;
    const caption = document.querySelector(`[data-item-id="${CSS.escape(item.id)}"] .tile__cap-title`);
    if (caption) caption.textContent = item.title;
  }

  titleInput.addEventListener("blur", () => void saveTitle());
  titleInput.addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      titleInput.blur();
    }
  });

  /**
   * A move out of the folder the grid is filtered to leaves behind a tile
   * that no longer belongs in the view; drop it instead of letting the grid
   * lie until the next reload. folder_id 0 is the library root, which the
   * grid also spells "0", so the two compare directly.
   */
  function pruneMovedTile(item: ApiItem): void {
    const filter = document.querySelector<HTMLElement>('[data-island="grid"]')?.dataset.folder ?? "";
    if (filter === "" || filter === String(item.folder_id)) return;
    document.querySelector(`[data-item-id="${CSS.escape(item.id)}"]`)?.remove();
  }

  // Move the open item to another folder. PATCH /api/items/{id} already
  // accepts folder_id, where 0 means the library root, so this needs no
  // route of its own.
  folderSelect?.addEventListener("change", async () => {
    if (!current) return;
    const item = current;
    const result = await apiSend<{ item: ApiItem }>(
      `/api/items/${encodeURIComponent(item.id)}`,
      "PATCH",
      { folder_id: Number(folderSelect.value) },
      "That move did not save.",
    );
    if (!result.ok) {
      showError(result.message);
      // Put the control back where the item actually is, so it never shows a
      // folder the item was not moved to. This runs for a rejected move and
      // for a request that never reached the server alike, which is the whole
      // point of apiSend collapsing both into one failure shape.
      folderSelect.value = String(item.folder_id ?? 0);
      return;
    }
    current = result.body.item;
    clearError();
    pruneMovedTile(result.body.item);
  });

  root.querySelector('[data-action="copy"]')?.addEventListener("click", async () => {
    if (!current) return;
    await navigator.clipboard.writeText(current.share_url);
  });

  root.querySelector('[data-action="delete"]')?.addEventListener("click", async () => {
    if (!current) return;
    if (!window.confirm(`Delete "${current.title}"? It goes to the trash.`)) return;
    const id = current.id;
    const uploaderLabel = uploaderName(current);
    // Nothing is removed from the grid until the server confirms, so a
    // failure of either kind leaves the view untouched and only needs to say
    // so. 403 keeps its own wording: it is a legitimate answer, not a fault.
    const result = await apiSend<unknown>(`/api/items/${encodeURIComponent(id)}`, "DELETE", undefined, "Delete failed.");
    if (!result.ok) {
      showError(
        result.status === 403
          ? `Only ${uploaderLabel} or an admin can delete this. Ask them, or hide it from your view instead.`
          : result.message,
      );
      return;
    }
    document.querySelector(`[data-item-id="${CSS.escape(id)}"]`)?.remove();
    close();
  });

  for (const button of root.querySelectorAll('[data-action="close"]')) {
    button.addEventListener("click", () => close());
  }
  document.addEventListener("keydown", (event) => {
    if (root.hasAttribute("hidden")) return;
    if (event.key === "Escape") {
      close();
      return;
    }
    // Trap Tab within the panel while the dialog is open, so it never
    // leaks focus into the grid behind it.
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

  // Event delegation, so tiles added by the grid island work without rebinding.
  document.addEventListener("click", (event) => {
    const target = (event.target as HTMLElement | null)?.closest<HTMLElement>('[data-action="open"]');
    if (!target) return;
    const id = target.closest<HTMLElement>("[data-item-id]")?.dataset.itemId;
    if (id) void open(id, true, target);
  });

  window.addEventListener("popstate", () => {
    const id = new URL(window.location.href).searchParams.get("item");
    if (id) void open(id, false);
    else close(false);
  });

  // Deep link: /?item=<id> opens straight into the viewer.
  const initial = new URL(window.location.href).searchParams.get("item");
  if (initial) void open(initial, false);
}
