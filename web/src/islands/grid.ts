/**
 * Infinite scroll over GET /api/items using the opaque keyset cursor the
 * server hands back. Offset pagination would duplicate and skip rows as
 * friends upload while someone is scrolling.
 */

import { mountTilePreview } from "./preview";
import { aspectRatio, formatBytes, formatDuration } from "../format";
import { pollItemUntilReady } from "../processing";
import { notify } from "../notify";

const GRID_SIZE_KEY = "boobies-media:grid-size";
const GRID_SIZE_MIN = 120;
const GRID_SIZE_MAX = 360;

export interface ApiItem {
  id: string;
  title: string;
  mime: string;
  ext: string;
  size: number;
  width: number;
  height: number;
  duration: number;
  uploader: number;
  folder_id: number;
  is_video: boolean;
  is_gif: boolean;
  ready: boolean;
  revoked: boolean;
  created_at: string;
  share_url: string;
  media_url: string;
  thumb_url: string;
  source_url: string;
  tags: string[];
}

interface ItemsPage {
  items: ApiItem[];
  next_cursor: string;
}

export function mountGrid(root: HTMLElement): void {
  mountGridSizeControl();
  const status = document.querySelector<HTMLElement>('[data-role="grid-status"]');
  const count = document.querySelector<HTMLElement>('[data-role="grid-count"]');
  let cursor = root.dataset.cursor ?? "";
  const sort = root.dataset.sort ?? "newest";
  // The active folder/tag/uploader/search filters, carried on the grid root
  // by the server (see browse.html) so infinite scroll keeps requesting the
  // same filtered set instead of silently falling back to everything once
  // the server-rendered first page runs out.
  const folder = root.dataset.folder ?? "";
  const tag = root.dataset.tag ?? "";
  const uploader = root.dataset.uploader ?? "";
  const q = root.dataset.q ?? "";
  let loading = false;
  let loaded = root.querySelectorAll('[data-role="tile"]').length;

  function updateCount(): void {
    if (count) count.textContent = `${loaded} loaded`;
  }

  async function loadMore(): Promise<void> {
    if (loading || !cursor) return;
    loading = true;
    status?.removeAttribute("hidden");
    try {
      const params = new URLSearchParams({ cursor, sort });
      if (folder) params.set("folder", folder);
      if (tag) params.set("tag", tag);
      if (uploader) params.set("uploader", uploader);
      if (q) params.set("q", q);
      const response = await fetch(`/api/items?${params.toString()}`, {
        headers: { Accept: "application/json" },
      });
      if (!response.ok) throw new Error(`items request failed: ${response.status}`);
      const page = (await response.json()) as ItemsPage;
      for (const item of page.items) root.appendChild(renderTile(item));
      loaded += page.items.length;
      updateCount();
      cursor = page.next_cursor ?? "";
    } catch (err) {
      console.error(err);
      // Stop retrying on every scroll event after a failure.
      cursor = "";
      if (status) status.textContent = "Could not load more. Scroll or reload to retry.";
    } finally {
      loading = false;
      if (cursor) status?.setAttribute("hidden", "");
    }
  }

  // Server-rendered tiles are in the DOM before this script runs; wire their
  // hover/focus previews the same way freshly-rendered tiles get wired below.
  for (const tile of root.querySelectorAll<HTMLElement>('[data-role="tile"]')) {
    mountTilePreview(tile);
    if (tile.classList.contains("tile--processing") && tile.dataset.itemId) {
      watchProcessingTile(tile, tile.dataset.itemId);
    }
  }

  // A sentinel below the grid is cheaper and less jittery than scroll maths.
  const sentinel = document.createElement("div");
  sentinel.className = "grid__sentinel";
  root.after(sentinel);

  const observer = new IntersectionObserver(
    (entries) => {
      if (entries.some((entry) => entry.isIntersecting)) void loadMore();
    },
    { rootMargin: "600px" },
  );
  observer.observe(sentinel);

  // Keep the visible first page current while background jobs finish or
  // friends add media. This is intentionally a small newest-page request,
  // not a reset of infinite-scroll state, so the user's scroll position and
  // already-loaded history stay intact.
  async function refreshVisibleItems(): Promise<void> {
    if (document.hidden || loading) return;
    const params = new URLSearchParams({ sort, limit: "24" });
    if (folder) params.set("folder", folder);
    if (tag) params.set("tag", tag);
    if (uploader) params.set("uploader", uploader);
    if (q) params.set("q", q);
    try {
      const response = await fetch(`/api/items?${params.toString()}`, {
        headers: { Accept: "application/json" },
      });
      if (!response.ok) return;
      const page = (await response.json()) as ItemsPage;
      let added = 0;
      for (const item of [...page.items].reverse()) {
        const existing = root.querySelector<HTMLElement>(`[data-item-id="${CSS.escape(item.id)}"]`);
        if (existing) {
          if (item.ready && existing.classList.contains("tile--processing")) {
            existing.replaceWith(renderTile(item));
          }
          continue;
        }
        root.prepend(renderTile(item));
        added++;
      }
      if (added > 0) {
        root.querySelector('[data-role="empty"]')?.remove();
        loaded += added;
        updateCount();
      }
    } catch {
      // A transient refresh failure should not disturb the working library.
    }
  }

  const refreshTimer = window.setInterval(() => void refreshVisibleItems(), 5000);
  window.addEventListener("pagehide", () => window.clearInterval(refreshTimer), { once: true });
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) void refreshVisibleItems();
  });
}

function mountGridSizeControl(): void {
  const input = document.querySelector<HTMLInputElement>('[data-action="grid-size"]');
  const output = document.querySelector<HTMLOutputElement>('[data-role="grid-size-value"]');
  if (!input || !output) return;

  let saved = Number.NaN;
  try {
    saved = Number(window.localStorage.getItem(GRID_SIZE_KEY));
  } catch {
    // Storage is optional; the control still works for this page view.
  }
  const fallback = window.matchMedia("(max-width: 720px)").matches ? 140 : 240;
  const initial = Number.isFinite(saved) && saved >= GRID_SIZE_MIN && saved <= GRID_SIZE_MAX ? saved : fallback;

  function apply(value: number): void {
    const clamped = Math.min(GRID_SIZE_MAX, Math.max(GRID_SIZE_MIN, value));
    document.documentElement.style.setProperty("--row-h", `${clamped}px`);
    input!.value = String(clamped);
    output!.value = `${clamped}px`;
  }

  apply(initial);
  input.addEventListener("input", () => apply(Number(input.value)));
  input.addEventListener("change", () => {
    const value = Number(input.value);
    try {
      window.localStorage.setItem(GRID_SIZE_KEY, String(value));
    } catch {
      // Keep the applied value even when it cannot be persisted.
    }
    notify(`Grid size set to ${value}px.`);
  });
}

export function renderTile(item: ApiItem): HTMLElement {
  const li = document.createElement("li");
  li.className = item.ready ? "tile" : "tile tile--processing";
  li.setAttribute("role", "listitem");
  li.dataset.itemId = item.id;
  li.dataset.role = "tile";
  // Read by mountTilePreview (both here and for server-rendered tiles) to
  // decide whether this tile has a hover preview and, if so, its source.
  li.dataset.mime = item.mime;
  li.dataset.mediaUrl = item.media_url;
  li.dataset.animated = String(item.is_gif);
  // Drives the justified grid's per-line flex-grow share; see main.css's
  // .tile and internal/web/templatefuncs.go's aspectRatio for the
  // server-rendered equivalent.
  li.style.setProperty("--aspect", String(aspectRatio(item.width, item.height)));

  // Selection checkbox (see bulkselect.ts). Present on every tile, not just
  // the ones the server renders, so a tile that only appears after
  // scrolling is just as selectable as one that was there on load; hidden
  // by CSS until selection mode is on.
  const selectLabel = document.createElement("label");
  selectLabel.className = "tile__select";
  const selectBox = document.createElement("input");
  selectBox.type = "checkbox";
  selectBox.dataset.action = "select-item";
  selectBox.setAttribute("aria-label", `Select ${item.title}`);
  selectLabel.appendChild(selectBox);
  li.appendChild(selectLabel);

  const button = document.createElement("button");
  button.type = "button";
  button.className = "tile__button";
  button.dataset.action = "open";
  button.setAttribute("aria-label", `Open ${item.title}`);

  const img = document.createElement("img");
  img.className = "tile__image";
  // Deliberately relative, not item.thumb_url: that field is an absolute
  // baseURL-prefixed URL meant for API consumers, and could disagree with
  // the origin the page itself is actually served from (e.g. behind a
  // proxy). The existing /t/{id} route is same-origin and unaffected by
  // that mismatch, exactly as it was before this change.
  img.src = `/t/${item.id}?s=320`;
  img.alt = "";
  img.loading = "lazy";
  img.width = 320;
  img.height = 320;
  button.appendChild(img);

  if (!item.ready) {
    const processing = document.createElement("span");
    processing.className = "tile__processing";
    processing.setAttribute("aria-hidden", "true");
    const label = document.createElement("span");
    label.className = "tile__processing-label";
    label.textContent = "PROCESSING";
    const meter = document.createElement("span");
    meter.className = "tile__processing-meter";
    const name = document.createElement("span");
    name.className = "tile__processing-name";
    name.textContent = item.title;
    processing.append(label, meter, name);
    button.appendChild(processing);
    li.append(button);
    watchProcessingTile(li, item.id);
    return li;
  }

  if (item.is_video) {
    const badge = document.createElement("span");
    badge.className = "tile__badge";
    badge.textContent = `▶ ${formatDuration(item.duration)}`;
    button.appendChild(badge);
  } else if (item.is_gif) {
    const badge = document.createElement("span");
    badge.className = "tile__badge";
    badge.textContent = item.mime === "image/gif" ? "GIF" : "WEBP";
    button.appendChild(badge);
  }

  const cap = document.createElement("span");
  cap.className = "tile__cap";
  const capTitle = document.createElement("span");
  capTitle.className = "tile__cap-title";
  // textContent, never innerHTML: titles are user input.
  capTitle.textContent = item.title;
  const capMeta = document.createElement("span");
  capMeta.className = "tile__cap-meta";
  const capSize = document.createElement("span");
  capSize.textContent = item.is_video ? `${formatDuration(item.duration)} · ${formatBytes(item.size)}` : formatBytes(item.size);
  const capDims = document.createElement("span");
  capDims.textContent = item.width ? `${item.width}×${item.height}` : "";
  capMeta.append(capSize, capDims);
  cap.append(capTitle, capMeta);
  button.appendChild(cap);

  li.append(button);
  mountTilePreview(li);
  return li;
}

function watchProcessingTile(tile: HTMLElement, itemID: string): void {
  void pollItemUntilReady(itemID).then((item) => {
    if (!item || !tile.isConnected) return;
    tile.replaceWith(renderTile(item));
  });
}
