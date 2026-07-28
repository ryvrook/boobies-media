/**
 * Folder management for the browse page.
 *
 * Folder *navigation* is not here: the rail this island attaches to is a
 * list of plain server-rendered links (see browse.html), so filtering by
 * folder keeps working with JavaScript off. This island only adds what has
 * no no-JavaScript equivalent, create, rename, move and delete, behind a
 * "Manage" button it appends to the rail heading.
 *
 * The controls live in a dialog rather than as per-row buttons in the rail
 * for two reasons: the rail's rows are anchors, and a button cannot be
 * nested inside one; and per-row actions would have to be revealed on hover
 * to fit, which is unreachable by keyboard. The dialog reuses admin.ts's
 * buildModal, so it inherits the same modal semantics lightbox.ts and
 * folder-sheet.ts establish: role="dialog", aria-modal, a focus trap,
 * Escape to close and focus restored to the opener.
 *
 * After a successful mutation the page reloads instead of the DOM being
 * patched. The rail, the breadcrumb path, the mobile folder sheet, the
 * lightbox's folder select and the grid itself are all rendered from the
 * same server state; hand-patching five places would drift from it the
 * first time any of them changes.
 */

import { buildModal, el } from "./admin";
import { apiSend } from "../request";
import { notifyAfterReload } from "../notify";

interface ApiFolder {
  id: number;
  parent_id: number;
  name: string;
}

/** A folder plus its depth in the tree, for indentation. */
interface FolderRow {
  folder: ApiFolder;
  depth: number;
}

/** Root-first, depth-annotated, siblings sorted by name. */
function flatten(folders: ApiFolder[]): FolderRow[] {
  const children = new Map<number, ApiFolder[]>();
  for (const folder of folders) {
    const siblings = children.get(folder.parent_id) ?? [];
    siblings.push(folder);
    children.set(folder.parent_id, siblings);
  }
  const rows: FolderRow[] = [];
  const walk = (parentId: number, depth: number): void => {
    const siblings = (children.get(parentId) ?? []).slice().sort((a, b) => a.name.localeCompare(b.name));
    for (const folder of siblings) {
      rows.push({ folder, depth });
      walk(folder.id, depth + 1);
    }
  };
  walk(0, 0);
  return rows;
}

export function mountFolders(root: HTMLElement): void {
  const heading = root.querySelector<HTMLElement>(".rails__heading");
  if (!heading) return;

  // The folder the page is currently filtered to, empty at the library root.
  const current = root.dataset.current ?? "";

  const modal = buildModal("Manage folders");
  const errorBox = el("p", "error-banner foldermgr__error");
  errorBox.setAttribute("role", "alert");
  errorBox.hidden = true;

  const trigger = el("button", "rails__manage", "Manage");
  trigger.type = "button";
  trigger.setAttribute("aria-haspopup", "dialog");
  trigger.title = "Create, rename, move or delete folders";
  heading.appendChild(trigger);
  trigger.addEventListener("click", () => void openManager());

  function showError(message: string): void {
    errorBox.textContent = message;
    errorBox.hidden = false;
  }

  function clearError(): void {
    errorBox.textContent = "";
    errorBox.hidden = true;
  }

  /**
   * Sends a mutation and reports the server's own message on failure. The
   * 409s matter most: a duplicate name and a move that would put a folder
   * inside itself are both things a user hits by accident, and the API
   * already words both, so apiSend never invents a message of its own when
   * one was sent.
   */
  async function send(url: string, method: string, body?: unknown): Promise<boolean> {
    const result = await apiSend<unknown>(url, method, body);
    if (result.ok) return true;
    showError(result.message);
    return false;
  }

  /** True when id is target, or sits somewhere beneath it. */
  function isWithin(id: number, target: number, byId: Map<number, ApiFolder>): boolean {
    let cursor: number | undefined = id;
    while (cursor !== undefined && cursor !== 0) {
      if (cursor === target) return true;
      cursor = byId.get(cursor)?.parent_id;
    }
    return false;
  }

  /**
   * Re-render by reloading. Deleting the folder the page is filtered to (or
   * one of its ancestors, since children cascade) would reload onto a
   * folder that no longer exists, so that case drops the filter instead.
   */
  function reload(message: string, deleted?: number, byId?: Map<number, ApiFolder>): void {
    notifyAfterReload(message);
    const activeId = Number(current);
    if (deleted !== undefined && byId && current !== "" && isWithin(activeId, deleted, byId)) {
      const url = new URL(window.location.href);
      url.searchParams.delete("folder");
      window.location.assign(url.toString());
      return;
    }
    window.location.reload();
  }

  /** A folder picker: the library root plus every folder except `skip`. */
  function folderPicker(rows: FolderRow[], selected: number, skip?: number): HTMLSelectElement {
    const select = el("select", "modal__input foldermgr__select");
    const library = el("option", undefined, "Library");
    library.value = "0";
    select.appendChild(library);
    for (const { folder, depth } of rows) {
      if (folder.id === skip) continue;
      const option = el("option", undefined, `${"\u00a0\u00a0".repeat(depth)}${folder.name}`);
      option.value = String(folder.id);
      select.appendChild(option);
    }
    select.value = String(selected);
    return select;
  }

  function createForm(rows: FolderRow[]): HTMLFormElement {
    const form = el("form", "foldermgr__create");
    const nameField = el("label", "modal__field");
    const nameInput = el("input", "modal__input");
    nameInput.type = "text";
    nameInput.placeholder = "Holidays";
    nameInput.required = true;
    nameField.append(el("span", undefined, "New folder"), nameInput);

    const parentField = el("label", "modal__field");
    // Default to the folder being browsed: creating a subfolder of where you
    // already are is the common case.
    const parent = folderPicker(rows, Number(current) || 0);
    parentField.append(el("span", undefined, "Inside"), parent);

    const submit = el("button", "btn", "Create");
    submit.type = "submit";
    form.append(nameField, parentField, submit);

    form.addEventListener("submit", (event) => {
      event.preventDefault();
      const name = nameInput.value.trim();
      if (!name) return;
      clearError();
      void send("/api/folders", "POST", { name, parent_id: Number(parent.value) }).then((ok) => {
        if (ok) reload(`Folder “${name}” created.`);
      });
    });
    return form;
  }

  function folderRow(row: FolderRow, rows: FolderRow[], byId: Map<number, ApiFolder>): HTMLElement {
    const { folder, depth } = row;
    const li = el("li", "foldermgr__row");
    const name = el("span", "foldermgr__name", folder.name);
    name.style.paddingLeft = `${depth * 12}px`;

    // Descendants stay in the list on purpose: the server answers a move
    // into one with a plain-language 409, which is a better teacher than an
    // option that silently is not there.
    const move = folderPicker(rows, folder.parent_id, folder.id);
    move.setAttribute("aria-label", `Move ${folder.name} into`);
    move.addEventListener("change", () => {
      clearError();
      void send(`/api/folders/${folder.id}`, "PATCH", { parent_id: Number(move.value) }).then((ok) => {
        if (ok) reload(`Folder “${folder.name}” moved.`);
        else move.value = String(folder.parent_id);
      });
    });

    const rename = el("button", "btn btn--quiet", "Rename");
    rename.type = "button";
    rename.setAttribute("aria-label", `Rename ${folder.name}`);
    rename.addEventListener("click", () => {
      const next = window.prompt("Rename folder", folder.name);
      if (next === null) return;
      const trimmed = next.trim();
      if (!trimmed || trimmed === folder.name) return;
      clearError();
      void send(`/api/folders/${folder.id}`, "PATCH", { name: trimmed }).then((ok) => {
        if (ok) reload(`Folder renamed to “${trimmed}”.`);
      });
    });

    const remove = el("button", "btn btn--danger", "Delete");
    remove.type = "button";
    remove.setAttribute("aria-label", `Delete ${folder.name}`);
    remove.addEventListener("click", () => {
      if (!window.confirm(`Delete "${folder.name}"? Subfolders go with it and anything inside moves back to the library.`)) return;
      clearError();
      void send(`/api/folders/${folder.id}`, "DELETE").then((ok) => {
        if (ok) reload(`Folder “${folder.name}” deleted.`, folder.id, byId);
      });
    });

    const actions = el("div", "foldermgr__actions");
    actions.append(move, rename, remove);
    li.append(name, actions);
    return li;
  }

  function render(folders: ApiFolder[]): void {
    const rows = flatten(folders);
    const byId = new Map(folders.map((folder) => [folder.id, folder]));
    const form = createForm(rows);
    const list = el("ul", "foldermgr__list");
    for (const row of rows) list.appendChild(folderRow(row, rows, byId));

    modal.body.replaceChildren(errorBox, form, rows.length === 0 ? el("p", "modal__text", "No folders yet.") : list);
    form.querySelector("input")?.focus();
  }

  async function openManager(): Promise<void> {
    clearError();
    modal.body.replaceChildren(el("p", "modal__text", "Loading folders..."));
    modal.open();
    const result = await apiSend<{ folders: ApiFolder[] }>(
      "/api/folders",
      "GET",
      undefined,
      "Folders could not be loaded.",
    );
    if (!result.ok) {
      showError(result.message);
      modal.body.replaceChildren(errorBox);
      return;
    }
    render(result.body.folders);
  }
}
