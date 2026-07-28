/**
 * Drag-and-drop and file-picker uploads, chunked and resumable, in a
 * non-modal panel toggled from the toolbar's Upload button.
 *
 * Every chunk is its own request, which is what lets a multi-gigabyte video
 * cross a Cloudflare Tunnel that caps any single body at 100 MB, and what
 * lets a dropped connection resume instead of starting over. The server owns
 * the chunk size; the client asks and obeys.
 */

import { renderTile, type ApiItem } from "./grid";
import { notify } from "../notify";

interface UploadStatus {
  upload_id: string;
  chunk_size: number;
  size: number;
  received: number[];
  missing: number[];
}

interface IngestResponse {
  item: ApiItem;
}

interface IngestError {
  error: string;
  code: string;
}

/** How many times one chunk is retried before the upload is called failed. */
const CHUNK_RETRIES = 3;

/**
 * The upload_id is remembered under a fingerprint of the file's own identity,
 * so choosing the same file again (after a reload, a closed tab, or a
 * crashed browser) resumes instead of re-sending bytes the server already
 * has. Storage is best-effort: private browsing or a full quota just means
 * that file starts over next time.
 */
const RESUME_STORAGE_PREFIX = "boobies-media:upload:";

/** How many bytes of the file's start are hashed into the resume key. */
const RESUME_SAMPLE_BYTES = 65536;

/**
 * Name, size, and last-modified time alone can collide: two different
 * files that happen to share all three would silently "resume" into the
 * same upload and splice both files' bytes together, with nothing
 * server-side to catch it (the server only checks total assembled size).
 * Hashing a content prefix makes that collision astronomically unlikely
 * without hashing the whole (possibly multi-gigabyte) file.
 *
 * Returns null if the prefix cannot be read or hashed (e.g. `crypto.subtle`
 * is unavailable in a non-secure context). Callers must treat null as "do
 * not resume": that always degrades safely to a fresh upload, whereas a
 * collision would silently corrupt an item.
 */
async function resumeKey(file: File): Promise<string | null> {
  try {
    const sample = await file.slice(0, RESUME_SAMPLE_BYTES).arrayBuffer();
    const digest = await window.crypto.subtle.digest("SHA-256", sample);
    const hex = Array.from(new Uint8Array(digest))
      .map((byte) => byte.toString(16).padStart(2, "0"))
      .join("");
    return `${RESUME_STORAGE_PREFIX}${file.name}:${file.size}:${file.lastModified}:${hex}`;
  } catch {
    return null;
  }
}

function readResumeID(key: string | null): string | null {
  if (!key) return null;
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeResumeID(key: string | null, uploadID: string): void {
  if (!key) return;
  try {
    window.localStorage.setItem(key, uploadID);
  } catch {
    // Non-fatal: the upload still completes, it just cannot resume later.
  }
}

function forgetResumeID(key: string | null): void {
  if (!key) return;
  try {
    window.localStorage.removeItem(key);
  } catch {
    // ignore
  }
}

type RowStatus = "waiting" | "uploading" | "assembling" | "done" | "failed" | "rejected" | "too_large" | "cancelled";

interface RowAction {
  label: string;
  variant?: "retry" | "danger";
  onClick: () => void;
}

interface RowOptions {
  status: RowStatus;
  statusText: string;
  pct?: number;
  detail?: string;
  note?: string;
  actions?: RowAction[];
}

/** (Re)builds one queue row's contents from scratch. Every piece of text is
 * set via textContent, never innerHTML: a filename is attacker-controlled
 * input. */
function renderRow(row: HTMLLIElement, name: string, opts: RowOptions): void {
  row.dataset.status = opts.status;
  row.replaceChildren();

  const line = document.createElement("div");
  line.className = "uploads__line";

  const nameEl = document.createElement("span");
  nameEl.className = "uploads__name";
  nameEl.textContent = name;
  line.appendChild(nameEl);

  if (opts.pct !== undefined) {
    const pct = document.createElement("span");
    pct.className = "uploads__pct";
    pct.textContent = `${opts.pct}%`;
    line.appendChild(pct);
  } else {
    const status = document.createElement("span");
    status.className = "uploads__status" + (opts.status === "failed" || opts.status === "too_large" || opts.status === "rejected" ? " uploads__status--error" : opts.status === "done" ? " uploads__status--done" : "");
    status.textContent = opts.statusText;
    line.appendChild(status);
  }

  for (const action of opts.actions ?? []) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "uploads__action" + (action.variant === "retry" ? " uploads__action--retry" : action.variant === "danger" ? " uploads__action--danger" : "");
    btn.textContent = action.label;
    btn.addEventListener("click", action.onClick);
    line.appendChild(btn);
  }
  row.appendChild(line);

  if (opts.pct !== undefined) {
    const meter = document.createElement("span");
    meter.className = "uploads__meter";
    const fill = document.createElement("span");
    fill.className = "uploads__meter-fill";
    fill.style.width = `${opts.pct}%`;
    meter.appendChild(fill);
    row.appendChild(meter);
  }
  if (opts.detail) {
    const detail = document.createElement("span");
    detail.className = "uploads__detail";
    detail.textContent = opts.detail;
    row.appendChild(detail);
  }
  if (opts.note) {
    const note = document.createElement("span");
    note.className = "uploads__note";
    note.textContent = opts.note;
    row.appendChild(note);
  }
}

export function mountUploader(root: HTMLElement): void {
  const input = root.querySelector<HTMLInputElement>("#file-input");
  const list = root.querySelector<HTMLElement>('[data-role="progress"]');
  const dropzone = root.querySelector<HTMLElement>('[data-role="dropzone"]');
  const dropzoneTitle = root.querySelector<HTMLElement>('[data-role="dropzone-title"]');
  const summary = root.querySelector<HTMLElement>('[data-role="summary"]');
  const grid = document.querySelector<HTMLElement>('[data-island="grid"]');
  const trigger = document.querySelector<HTMLButtonElement>('[data-action="toggle-uploader"]');
  const queueCount = document.querySelector<HTMLElement>('[data-role="queue-count"]');
  // The folder currently being browsed, if any (see browse.html), so a
  // file dropped while looking at "holiday / 2026" lands there rather than
  // always at the root. There is no folder-picker in this panel: moving an
  // upload's destination beyond that default is out of scope (no route).
  const activeFolder = root.dataset.activeFolder ? Number(root.dataset.activeFolder) : 0;
  if (!input || !list) return;

  const dropzoneDefaultTitle = dropzoneTitle?.textContent ?? "Drop files here";
  const allowedMimes = new Set(
    (input.getAttribute("accept") ?? "")
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean),
  );

  function openPanel(): void {
    root.hidden = false;
    requestAnimationFrame(() => root.setAttribute("data-open", ""));
    trigger?.setAttribute("aria-expanded", "true");
  }
  function closePanel(): void {
    root.removeAttribute("data-open");
    trigger?.setAttribute("aria-expanded", "false");
    window.setTimeout(() => {
      if (!root.hasAttribute("data-open")) root.hidden = true;
    }, 180);
  }
  trigger?.addEventListener("click", () => {
    if (root.hidden) openPanel();
    else closePanel();
  });
  for (const button of root.querySelectorAll('[data-action="close"]')) {
    button.addEventListener("click", () => closePanel());
  }
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !root.hidden) closePanel();
  });
  // The empty-state "Choose files" button on the grid opens straight to the
  // file picker.
  document.querySelector('[data-action="empty-upload"]')?.addEventListener("click", () => {
    openPanel();
    input.click();
  });

  root.querySelector<HTMLButtonElement>('[data-action="pick"]')?.addEventListener("click", () => input.click());
  input.addEventListener("change", () => {
    if (input.files) void uploadAll(Array.from(input.files));
    input.value = "";
  });

  function refreshSummary(): void {
    const rows = Array.from(list!.querySelectorAll<HTMLLIElement>(".uploads__row"));
    const total = rows.length;
    const done = rows.filter((r) => r.dataset.status === "done").length;
    const active = rows.filter((r) => r.dataset.status === "uploading" || r.dataset.status === "waiting" || r.dataset.status === "assembling").length;
    if (summary) {
      if (total === 0) {
        summary.hidden = true;
      } else {
        summary.hidden = false;
        summary.textContent = `${done} of ${total} done`;
      }
    }
    if (queueCount) {
      if (active === 0) {
        queueCount.hidden = true;
      } else {
        queueCount.hidden = false;
        queueCount.textContent = String(active);
      }
    }
  }

  // Whole-window drag and drop, so a file can be dropped anywhere, and the
  // panel opens to show the drop target and the live file count.
  for (const type of ["dragenter", "dragover"]) {
    window.addEventListener(type, (event) => {
      event.preventDefault();
      document.body.classList.add("dragging");
      const count = (event as DragEvent).dataTransfer?.items.length ?? 0;
      if (dropzoneTitle) {
        dropzoneTitle.textContent = count > 0 ? `Release to add ${count} file${count === 1 ? "" : "s"}` : dropzoneDefaultTitle;
      }
      dropzone?.classList.add("dropzone--active");
      if (root.hidden) openPanel();
    });
  }
  for (const type of ["dragleave", "drop"]) {
    window.addEventListener(type, (event) => {
      event.preventDefault();
      document.body.classList.remove("dragging");
      dropzone?.classList.remove("dropzone--active");
      if (dropzoneTitle) dropzoneTitle.textContent = dropzoneDefaultTitle;
    });
  }
  window.addEventListener("drop", (event) => {
    const files = (event as DragEvent).dataTransfer?.files;
    if (files && files.length > 0) void uploadAll(Array.from(files));
  });

  const urlForm = root.querySelector<HTMLFormElement>('[data-role="urlform"]');
  urlForm?.addEventListener("submit", (event) => {
    event.preventDefault();
    const field = urlForm.querySelector<HTMLInputElement>('input[name="url"]');
    const url = field?.value.trim();
    if (!url) return;
    if (field) field.value = "";
    openPanel();
    void ingestURL(url);
  });

  interface JobStatus {
    status: string;
    error: string;
    items: ApiItem[];
  }

  function shortURL(url: string): string {
    try {
      const parsed = new URL(url);
      const suffix = parsed.pathname.length > 24 ? `${parsed.pathname.slice(0, 24)}…` : parsed.pathname;
      return parsed.hostname + suffix;
    } catch {
      return url.slice(0, 40);
    }
  }

  async function ingestURL(url: string): Promise<void> {
    const row = document.createElement("li");
    row.className = "uploads__row";
    list!.appendChild(row);
    const label = shortURL(url);
    renderRow(row, label, { status: "waiting", statusText: "submitting" });
    refreshSummary();

    try {
      const response = await fetch("/api/ingest", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ url }),
      });
      if (!response.ok) {
        const detail = (await response.json().catch(() => null)) as IngestError | null;
        renderRow(row, label, { status: "failed", statusText: detail?.error ?? `failed (${response.status})` });
        refreshSummary();
        return;
      }
      const accepted = (await response.json()) as { job_id: number };
      await pollJob(accepted.job_id, label, row);
    } catch {
      renderRow(row, label, { status: "failed", statusText: "the server could not be reached" });
      refreshSummary();
    }
  }

  async function pollJob(jobID: number, label: string, row: HTMLLIElement): Promise<void> {
    const deadline = Date.now() + 10 * 60 * 1000;
    renderRow(row, label, { status: "uploading", statusText: "downloading" });
    refreshSummary();
    while (Date.now() < deadline) {
      await new Promise((resolve) => window.setTimeout(resolve, 2000));
      let response: Response;
      try {
        response = await fetch(`/api/jobs/${jobID}`, { headers: { Accept: "application/json" } });
      } catch {
        continue;
      }
      if (!response.ok) continue;
      const job = (await response.json()) as JobStatus;
      if (job.status === "done") {
        for (const item of [...job.items].reverse()) grid?.prepend(renderTile(item));
        grid?.querySelector('[data-role="empty"]')?.remove();
        renderRow(row, label, {
          status: "done",
          statusText: `done (${job.items.length} item${job.items.length === 1 ? "" : "s"})`,
        });
        notify(`${job.items.length} item${job.items.length === 1 ? "" : "s"} added.`);
        refreshSummary();
        window.setTimeout(() => { row.remove(); refreshSummary(); }, 4000);
        return;
      }
      if (job.status === "failed") {
        renderRow(row, label, { status: "failed", statusText: job.error || "failed" });
        refreshSummary();
        return;
      }
      renderRow(row, label, {
        status: "uploading",
        statusText: job.error ? `retrying: ${job.error}` : "downloading",
      });
    }
    renderRow(row, label, { status: "failed", statusText: "still working; check the admin page later" });
    refreshSummary();
  }

  async function uploadAll(files: File[]): Promise<void> {
    openPanel();
    for (const file of files) queueOne(file);
  }

  /** Client-side type check, for the same immediate "this file will not be
   * accepted" feedback the design shows, without a round trip. A file whose
   * browser-sniffed type is empty or unrecognised is NOT rejected here: the
   * server's own sniff (internal/media/sniff.go) is the real authority, and
   * an empty File.type is common for perfectly valid files on some
   * OS/browser combinations. */
  function looksUnsupported(file: File): boolean {
    return file.type !== "" && allowedMimes.size > 0 && !allowedMimes.has(file.type);
  }

  function queueOne(file: File): void {
    const row = document.createElement("li");
    row.className = "uploads__row";
    list!.appendChild(row);

    if (looksUnsupported(file)) {
      const ext = file.name.includes(".") ? file.name.split(".").pop() : file.type;
      renderRow(row, file.name, {
        status: "rejected",
        statusText: `rejected · ${ext}`,
        actions: [{ label: "Remove", onClick: () => { row.remove(); refreshSummary(); } }],
      });
      refreshSummary();
      return;
    }

    renderRow(row, file.name, { status: "waiting", statusText: "waiting" });
    refreshSummary();
    void runUpload(file, row);
  }

  async function runUpload(file: File, row: HTMLLIElement): Promise<void> {
    const controller = new AbortController();
    let uploadID: string | null = null;

    renderRow(row, file.name, {
      status: "uploading",
      statusText: "starting",
      pct: 0,
      actions: [{ label: "Cancel", onClick: () => controller.abort() }],
    });
    refreshSummary();

    const fail = async (response: Response, status: RowStatus = "failed"): Promise<void> => {
      const detail = (await response.json().catch(() => null)) as IngestError | null;
      renderRow(row, file.name, {
        status,
        statusText: detail?.error ?? `failed (${response.status})`,
        actions: status === "too_large" ? [{ label: "Remove", onClick: () => { row.remove(); refreshSummary(); } }] : retryOrRemove(),
      });
      refreshSummary();
    };

    const retryOrRemove = (): RowAction[] => [
      { label: "Retry", variant: "retry", onClick: () => void runUpload(file, row) },
      { label: "Remove", onClick: () => { row.remove(); refreshSummary(); } },
    ];

    try {
      // 1. Resume a previous attempt at this exact file if the browser still
      //    remembers one; otherwise declare a new upload. Either way the
      //    server answers with the chunk size it wants and which chunks it
      //    already holds (empty for a brand-new upload).
      let status: UploadStatus | null = null;
      let resuming = false;
      const key = await resumeKey(file);
      const resumeID = readResumeID(key);
      if (resumeID) {
        const resumed = await fetch(`/api/uploads/${resumeID}`, {
          headers: { Accept: "application/json" },
        });
        if (resumed.ok) {
          status = (await resumed.json()) as UploadStatus;
          resuming = true;
        } else if (resumed.status === 404) {
          // The upload was completed, cancelled, or reaped since we last saw
          // it: this id is genuinely gone, so it is safe to stop tracking it.
          forgetResumeID(key);
        } else {
          // A transient failure (expired session, server hiccup) is not
          // proof the upload is gone. Forgetting the pointer here would
          // force a full restart of a possibly multi-gigabyte upload for no
          // reason: keep it, and let a later attempt (e.g. after a
          // re-login) resume from where this one left off.
          return await fail(resumed);
        }
      }
      if (!status) {
        const opened = await fetch("/api/uploads", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ filename: file.name, size: file.size, folder_id: activeFolder }),
          signal: controller.signal,
        });
        if (!opened.ok) return await fail(opened, opened.status === 413 ? "too_large" : "failed");
        status = (await opened.json()) as UploadStatus;
      }
      uploadID = status.upload_id;
      writeResumeID(key, status.upload_id);

      // 2. Send only what is missing. Slicing a File is lazy: the browser
      //    reads each slice off disk as it is sent, so a 4 GB file never
      //    lands in memory.
      const chunkTotal = Math.max(1, Math.ceil(status.size / status.chunk_size) || status.missing.length);
      const already = chunkTotal - status.missing.length;
      let sent = 0;
      for (const index of status.missing) {
        const start = index * status.chunk_size;
        const blob = file.slice(start, start + status.chunk_size);
        const ok = await putChunkWithRetry(status.upload_id, index, blob, controller.signal);
        if (!ok) {
          if (controller.signal.aborted) {
            renderRow(row, file.name, { status: "cancelled", statusText: "cancelled" });
            refreshSummary();
            void fetch(`/api/uploads/${status.upload_id}`, { method: "DELETE" }).catch(() => {});
            return;
          }
          // The upload_id stays remembered (see readResumeID above), so
          // retrying (or choosing this file again) resumes from here
          // instead of restarting.
          renderRow(row, file.name, {
            status: "failed",
            statusText: "connection lost",
            note: `Failed at chunk ${index + 1}/${chunkTotal}: held chunks are kept.`,
            actions: retryOrRemove(),
          });
          refreshSummary();
          return;
        }
        sent += 1;
        const pct = Math.round(((already + sent) / chunkTotal) * 100);
        renderRow(row, file.name, {
          status: "uploading",
          statusText: `${pct}%`,
          pct,
          detail: resuming && sent === 1 ? "resumed after reload" : `chunk ${already + sent}/${chunkTotal}`,
          actions: [{ label: "Cancel", onClick: () => controller.abort() }],
        });
      }

      // 3. Assemble. This is where the file becomes an item.
      renderRow(row, file.name, { status: "assembling", statusText: "assembling…" });
      const finished = await fetch(`/api/uploads/${status.upload_id}/complete`, { method: "POST" });
      if (!finished.ok) {
        if (finished.status === 404) {
          // The server has no memory of this id at all: completed and
          // reaped, cancelled, or simply expired. Nothing forgetting it
          // later would gain: choosing this file again should start a fresh
          // upload immediately rather than repeat this same dead end first.
          forgetResumeID(key);
        }
        return await fail(finished);
      }
      // A 200 here means the server already finished this exact upload_id
      // on an earlier request (this handler retried, another tab beat it,
      // whatever the cause) and is handing back that original item rather
      // than making a second one, still success from this file's point of
      // view, so it is handled identically to the normal 201 below.
      const result = (await finished.json()) as IngestResponse;
      forgetResumeID(key);

      renderRow(row, file.name, {
        status: "done",
        statusText: "done",
        detail: result.item.ready ? undefined : "processing · ready:false",
      });
      refreshSummary();
      // Show it immediately, before the probe and thumbnail jobs finish.
      grid?.querySelector('[data-role="empty"]')?.remove();
      grid?.prepend(renderTile(result.item));
      notify(`“${result.item.title}” added.`);
      window.setTimeout(() => { row.remove(); refreshSummary(); }, 4000);
    } catch (err) {
      if (controller.signal.aborted) {
        renderRow(row, file.name, { status: "cancelled", statusText: "cancelled" });
        if (uploadID) void fetch(`/api/uploads/${uploadID}`, { method: "DELETE" }).catch(() => {});
      } else {
        console.error(err);
        renderRow(row, file.name, { status: "failed", statusText: "upload failed", actions: retryOrRemove() });
      }
      refreshSummary();
    }
  }

  /**
   * PUT one chunk, retrying a few times with backoff.
   *
   * Retrying is safe because the server stores a chunk idempotently: re-sending
   * one it already has is a 204, not an error. A 4xx other than a timeout is
   * not retried: the server is telling us something we cannot fix by asking
   * again. Cancellation (an aborted signal) is not retried either.
   */
  async function putChunkWithRetry(uploadID: string, index: number, blob: Blob, signal: AbortSignal): Promise<boolean> {
    for (let attempt = 0; attempt < CHUNK_RETRIES; attempt++) {
      if (signal.aborted) return false;
      try {
        const response = await fetch(`/api/uploads/${uploadID}/${index}`, { method: "PUT", body: blob, signal });
        if (response.ok) return true;
        if (response.status < 500 && response.status !== 408) return false;
      } catch {
        if (signal.aborted) return false;
        // Network error: fall through to the backoff below.
      }
      await new Promise((resolve) => window.setTimeout(resolve, 500 * 2 ** attempt));
    }
    return false;
  }
}
