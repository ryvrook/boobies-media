import "./main.css";

import { mountGrid } from "./islands/grid";
import { mountUploader } from "./islands/uploader";
import { mountLightbox } from "./islands/lightbox";
import { mountFolderSheet } from "./islands/folder-sheet";
import { mountFolders } from "./islands/folders";
import { mountAdmin } from "./islands/admin";
import { mountCopy } from "./islands/copy";
import { mountBulkSelect } from "./islands/bulkselect";

/**
 * Island registry. Each island is a small behaviour attached to an element
 * carrying `data-island="<name>"`. Later plans register the tag editor and
 * bulk-select toolbar here.
 */
type IslandFactory = (el: HTMLElement) => void;

const islands = new Map<string, IslandFactory>();

export function registerIsland(name: string, factory: IslandFactory): void {
  islands.set(name, factory);
}

function mountIslands(root: ParentNode = document): void {
  for (const el of root.querySelectorAll<HTMLElement>("[data-island]")) {
    const name = el.dataset.island;
    if (!name) continue;
    const factory = islands.get(name);
    if (!factory) {
      console.warn(`unknown island: ${name}`);
      continue;
    }
    factory(el);
  }
}

registerIsland("grid", mountGrid);
registerIsland("uploader", mountUploader);
registerIsland("lightbox", mountLightbox);
registerIsland("folder-sheet", mountFolderSheet);
registerIsland("folders", mountFolders);
registerIsland("admin", mountAdmin);
registerIsland("copy", mountCopy);
registerIsland("bulkselect", mountBulkSelect);

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () => mountIslands());
} else {
  mountIslands();
}
