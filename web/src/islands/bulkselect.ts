/**
 * Multi-select toolbar over the browse grid: a "Select" toggle puts the grid
 * into selection mode, a checkbox on each tile (added here and by grid.ts's
 * renderTile for tiles appended during infinite scroll) tracks membership,
 * and the toolbar applies one action to the whole selection through
 * POST /api/items/batch.
 *
 * Interaction: while selecting, clicking a tile's own open button toggles
 * its checkbox instead of opening the lightbox (captured and stopped before
 * lightbox.ts's document-level click delegate ever sees it); clicking the
 * checkbox itself needs no special handling; it is a normal, keyboard-
 * operable control the browser already toggles, and this file only listens
 * for the resulting "change" event to keep the selection Set in sync.
 * Entering selection mode also stops whatever hover preview is currently
 * playing and (see preview.ts's own "selecting" guard) suppresses new ones
 * from starting, so a video isn't decoding, unseen, behind a bank of
 * checkboxes the moment the user starts picking tiles.
 */

import { stopActivePreview } from "./preview";
import { notify } from "../notify";

interface BatchResult {
  applied: number;
  ok: string[];
  failed: { id: string; error: string }[];
}

export function mountBulkSelect(root: HTMLElement): void {
  const toggle = root.querySelector<HTMLInputElement>('[data-action="select-mode"]');
  const count = root.querySelector<HTMLElement>('[data-role="count"]');
  const folderSelect = root.querySelector<HTMLSelectElement>('[data-role="bulk-folder"]');
  const moveBtn = root.querySelector<HTMLButtonElement>('[data-action="bulk-move"]');
  const copyBtn = root.querySelector<HTMLButtonElement>('[data-action="bulk-copy"]');
  const tagBtn = root.querySelector<HTMLButtonElement>('[data-action="bulk-tag"]');
  const deleteBtn = root.querySelector<HTMLButtonElement>('[data-action="bulk-delete"]');
  const selectAllBtn = root.querySelector<HTMLButtonElement>('[data-action="select-all-loaded"]');
  const grid = document.querySelector<HTMLElement>('[data-island="grid"]');
  if (!toggle || !count || !folderSelect || !moveBtn || !copyBtn || !tagBtn || !deleteBtn || !grid) return;

  root.removeAttribute("hidden");

  const selected = new Set<string>();
  let selecting = false;
  let allMatching = false;

  function tileFor(id: string): HTMLElement | null {
    return grid!.querySelector<HTMLElement>(`[data-item-id="${CSS.escape(id)}"]`);
  }

  function setTileSelected(tile: HTMLElement, on: boolean): void {
    tile.classList.toggle("tile--selected", on);
    const box = tile.querySelector<HTMLInputElement>('[data-action="select-item"]');
    if (box) box.checked = on;
  }

  function refresh(message?: string): void {
    count!.textContent = message ?? `${selected.size} selected`;
    for (const button of [moveBtn!, copyBtn!, tagBtn!, deleteBtn!]) button.disabled = selected.size === 0;
  }

  function setSelected(id: string, on: boolean): void {
    if (on) selected.add(id);
    else selected.delete(id);
    if (!on) allMatching = false;
    const tile = tileFor(id);
    if (tile) setTileSelected(tile, on);
  }

  function exitSelectMode(): void {
    for (const id of Array.from(selected)) setSelected(id, false);
    refresh();
  }

  toggle.addEventListener("change", () => {
    selecting = toggle.checked;
    document.body.classList.toggle("selecting", selecting);
    if (!selecting) exitSelectMode();
    else stopActivePreview(); // a preview mid-hover should not keep playing under the checkboxes
  });

  selectAllBtn?.addEventListener("click", async () => {
    if (allMatching) {
      allMatching = false;
      for (const id of Array.from(selected)) setSelected(id, false);
      selectAllBtn.textContent = "Select all matching";
      refresh();
      return;
    }
    selectAllBtn.disabled = true;
    selectAllBtn.textContent = "Selecting…";
    try {
      const params = new URLSearchParams();
      for (const key of ["folder", "tag", "uploader", "q", "sort", "type"] as const) {
        const value = grid.dataset[key];
        if (value) params.set(key, value);
      }
      const response = await fetch(`/api/items/ids?${params.toString()}`, {
        headers: { Accept: "application/json" },
      });
      if (!response.ok) throw new Error("selection request failed");
      const { ids } = (await response.json()) as { ids: string[] };
      selected.clear();
      for (const id of ids) selected.add(id);
      for (const tile of grid.querySelectorAll<HTMLElement>("[data-item-id]")) {
        const id = tile.dataset.itemId;
        if (id) setTileSelected(tile, selected.has(id));
      }
      allMatching = true;
      selectAllBtn.textContent = "Clear all";
      refresh();
    } catch {
      notify("Could not select the filtered library.", "error");
      selectAllBtn.textContent = "Select all matching";
    } finally {
      selectAllBtn.disabled = false;
    }
  });

  // Delegated so tiles grid.ts appends later (infinite scroll) work with no
  // extra wiring; capture phase claims the click before it can bubble to
  // lightbox.ts's document-level "open" listener.
  grid.addEventListener(
    "click",
    (event) => {
      if (!selecting) return;
      const openButton = (event.target as HTMLElement | null)?.closest<HTMLElement>('[data-action="open"]');
      if (!openButton) return; // a click on the checkbox itself needs no help here
      event.preventDefault();
      event.stopPropagation();
      const id = openButton.closest<HTMLElement>("[data-item-id]")?.dataset.itemId;
      if (!id) return;
      setSelected(id, !selected.has(id));
      refresh();
    },
    true,
  );

  // Delegated change listener for the per-tile checkboxes (both server-
  // rendered tiles and ones grid.ts appends later carry the same
  // data-action/data-item-id pair).
  grid.addEventListener("change", (event) => {
    const box = (event.target as HTMLElement | null)?.closest<HTMLInputElement>('[data-action="select-item"]');
    if (!box) return;
    const id = box.closest<HTMLElement>("[data-item-id]")?.dataset.itemId;
    if (!id) return;
    setSelected(id, box.checked);
    refresh();
  });

  function updateGridCount(): void {
    const gridCount = document.querySelector<HTMLElement>('[data-role="grid-count"]');
    if (gridCount) gridCount.textContent = `${grid!.querySelectorAll('[data-role="tile"]').length} loaded`;
  }

  async function apply(payload: Record<string, unknown>): Promise<void> {
    const ids = Array.from(selected);
    const result: BatchResult = { applied: 0, ok: [], failed: [] };
    for (let offset = 0; offset < ids.length; offset += 500) {
      const response = await fetch("/api/items/batch", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...payload, ids: ids.slice(offset, offset + 500) }),
      });
      if (!response.ok) {
        const detail = (await response.json().catch(() => null)) as { error?: string } | null;
        notify(detail?.error ?? "That bulk action failed.", "error");
        return;
      }
      const chunk = (await response.json()) as BatchResult;
      result.applied += chunk.applied;
      result.ok.push(...chunk.ok);
      result.failed.push(...chunk.failed);
    }
    const currentFolder = grid!.dataset.folder ?? "";
    for (const id of result.ok) {
      const tile = tileFor(id);
      if (!tile) continue;
      if (payload.action === "delete") {
        tile.remove();
      } else if (
        payload.action === "move" &&
        String(payload.folder_id) !== (currentFolder === "root" ? "0" : currentFolder)
      ) {
        // A move always removes the source tile when it leaves the folder
        // currently on screen, including the Library root.
        tile.remove();
      }
      selected.delete(id);
    }
    updateGridCount();
    const summary =
      result.failed.length === 0
        ? `${result.applied} item(s) updated.`
        : `${result.applied} item(s) updated, ${result.failed.length} could not be updated.`;
    refresh(summary);
    allMatching = false;
    if (selectAllBtn) selectAllBtn.textContent = "Select all matching";
    notify(summary, result.failed.length === 0 ? "success" : "info");
    // Give the aria-live announcement above a moment to be read before the
    // count reverts to the live "N selected" state.
    window.setTimeout(() => refresh(), 3000);
  }

  deleteBtn.addEventListener("click", () => {
    if (selected.size === 0) return;
    if (!window.confirm(`Delete ${selected.size} item(s)? They go to the trash.`)) return;
    void apply({ action: "delete" });
  });
  moveBtn.addEventListener("click", () => {
    if (selected.size === 0) return;
    void apply({ action: "move", folder_id: Number(folderSelect.value) });
  });
  copyBtn.addEventListener("click", () => {
    if (selected.size === 0) return;
    void apply({ action: "copy", folder_id: Number(folderSelect.value) });
  });
  tagBtn.addEventListener("click", () => {
    if (selected.size === 0) return;
    const tag = window.prompt("Tag to add to every selected item");
    if (!tag) return;
    void apply({ action: "tag", tag });
  });

  refresh();

  // Infinite scroll can append a nearby tile after a filter-wide selection.
  // Reflect its already-selected ID without causing any additional loading.
  new MutationObserver((records) => {
    for (const record of records) {
      for (const node of record.addedNodes) {
        if (!(node instanceof HTMLElement)) continue;
        const id = node.dataset.itemId;
        if (id && selected.has(id)) setTileSelected(node, true);
      }
    }
  }).observe(grid, { childList: true });
}
