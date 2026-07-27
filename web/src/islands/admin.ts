/**
 * Admin dashboard actions. One delegated click listener drives the user, job
 * and trash buttons rendered server-side (see admin.html), so no per-row
 * wiring is needed. Most actions fetch then reload the server-rendered page;
 * the two that mint a one-time API key show it in a dialog instead, because
 * a reload would lose it and it can never be fetched again. Purge gets its
 * own typed-confirmation dialog rather than a plain confirm(), because it is
 * the one action here that is both destructive and irreversible.
 */

import { bindCopyButton } from "./copy";

/** Tiny element-builder to keep the dialog-construction code below terse. */
export function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  if (text !== undefined) node.textContent = text;
  return node;
}

let modalSeq = 0;

export interface Modal {
  body: HTMLElement;
  open(initial?: HTMLElement | null): void;
  close(): void;
}

/**
 * A minimal centred dialog: backdrop click, the close button and Escape all
 * close it; Tab is trapped inside the panel; focus moves in on open and
 * back to whatever opened it on close. Mirrors the conventions lightbox.ts
 * and folder-sheet.ts already use, factored here so the two dialogs this
 * island needs (the API key reveal and the purge confirmation) do not
 * duplicate that machinery. Exported because folders.ts needs the same
 * dialog; if a third island wants it, move it to its own module.
 */
export function buildModal(titleText: string, onClose?: () => void): Modal {
  const overlay = el("div", "modal");
  overlay.hidden = true;
  const backdrop = el("div", "modal__backdrop");
  backdrop.dataset.action = "close";
  const panel = el("div", "modal__panel");
  panel.setAttribute("role", "dialog");
  panel.setAttribute("aria-modal", "true");
  const heading = el("h2", "modal__title", titleText);
  heading.id = `modal-title-${++modalSeq}`;
  panel.setAttribute("aria-labelledby", heading.id);
  const body = el("div", "modal__body");
  const closeButton = el("button", "modal__close", "×");
  closeButton.type = "button";
  closeButton.dataset.action = "close";
  closeButton.setAttribute("aria-label", "Close");
  panel.append(heading, body, closeButton);
  overlay.append(backdrop, panel);
  document.body.appendChild(overlay);

  let opener: HTMLElement | null = null;
  const focusable = (): HTMLElement[] =>
    Array.from(
      panel.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])'),
    ).filter((node) => !node.hasAttribute("hidden"));

  function open(initial?: HTMLElement | null): void {
    opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    overlay.hidden = false;
    (initial ?? focusable()[0])?.focus();
  }
  function close(): void {
    overlay.hidden = true;
    if (opener && document.contains(opener)) opener.focus();
    opener = null;
    onClose?.();
  }
  overlay.addEventListener("click", (event) => {
    if ((event.target as HTMLElement).closest('[data-action="close"]')) close();
  });
  document.addEventListener("keydown", (event) => {
    if (overlay.hidden) return;
    if (event.key === "Escape") {
      close();
      return;
    }
    if (event.key !== "Tab") return;
    const items = focusable();
    if (items.length === 0) return;
    const first = items[0]!;
    const last = items[items.length - 1]!;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  });

  return { body, open, close };
}

export function mountAdmin(root: HTMLElement): void {
  async function send(url: string, method: string, body?: unknown): Promise<Response> {
    return fetch(url, {
      method,
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  }
  async function failMessage(response: Response): Promise<string> {
    const detail = (await response.json().catch(() => null)) as { error?: string } | null;
    return detail?.error ?? `failed (${response.status})`;
  }
  async function reloadOrAlert(response: Response): Promise<void> {
    if (response.ok) window.location.reload();
    else window.alert(await failMessage(response));
  }

  // The one-time API key reveal. Body content is cleared on every close path
  // (backdrop, Escape, the close button) so the plaintext key never lingers
  // in the DOM once the admin has dismissed it; it is never re-fetched or
  // re-shown after that.
  const keyModal = buildModal("New API key", () => keyModal.body.replaceChildren());

  function showKey(key: string, context: string): void {
    keyModal.body.replaceChildren();
    const warn = el("p", "banner banner--warn", "This is the only time this key will be shown. Copy it now.");
    const code = el("code", "modal__key", key);
    const copyButton = el("button", "btn", "Copy key");
    copyButton.type = "button";
    bindCopyButton(copyButton, () => key);
    const note = el("p", "modal__text", context);
    keyModal.body.append(warn, code, copyButton, note);
    // Moving focus into a dialog whose accessible name is "New API key" is
    // itself the announcement to a screen reader; no separate live region
    // is needed on top of that.
    keyModal.open(copyButton);
  }

  // The purge confirmation. Disabled until the admin types "purge", so
  // purging can never be one accidental click next to Restore.
  const purgeModal = buildModal("Confirm purge");

  function confirmPurge(id: string, title: string): void {
    purgeModal.body.replaceChildren();
    const text = el("p", "modal__text", `Permanently delete "${title}"? This cannot be undone.`);

    const field = el("label", "modal__field");
    const input = el("input", "modal__input");
    input.type = "text";
    input.placeholder = "purge";
    field.append(el("span", undefined, "Type purge to confirm"), input);

    const actions = el("div", "modal__actions");
    const cancel = el("button", "btn btn--quiet", "Cancel");
    cancel.type = "button";
    cancel.dataset.action = "close";
    const purge = el("button", "btn btn--danger", "Purge");
    purge.type = "button";
    purge.disabled = true;
    actions.append(cancel, purge);

    input.addEventListener("input", () => {
      purge.disabled = input.value.trim().toLowerCase() !== "purge";
    });
    purge.addEventListener("click", () => {
      purgeModal.close();
      void send(`/api/admin/items/${encodeURIComponent(id)}/purge`, "DELETE").then(reloadOrAlert);
    });

    purgeModal.body.append(text, field, actions);
    purgeModal.open(input);
  }

  root.addEventListener("click", (event) => {
    const button = (event.target as HTMLElement | null)?.closest<HTMLElement>("[data-action]");
    if (!button) return;
    const userRow = button.closest<HTMLElement>("[data-user-id]");
    const jobRow = button.closest<HTMLElement>("[data-job-id]");
    const itemRow = button.closest<HTMLElement>("[data-item-id]");

    switch (button.dataset.action) {
      case "toggle-admin":
        if (userRow) {
          const makeAdmin = userRow.dataset.isAdmin !== "true";
          void send(`/api/admin/users/${userRow.dataset.userId}`, "PATCH", { is_admin: makeAdmin }).then(reloadOrAlert);
        }
        break;
      case "reset-password": {
        if (!userRow) break;
        const password = window.prompt("New password");
        if (password) void send(`/api/admin/users/${userRow.dataset.userId}/password`, "POST", { password }).then(reloadOrAlert);
        break;
      }
      case "rotate-key":
        if (userRow) {
          void send(`/api/admin/users/${userRow.dataset.userId}/apikey`, "POST").then(async (response) => {
            if (!response.ok) {
              window.alert(await failMessage(response));
              return;
            }
            const { api_key } = (await response.json()) as { api_key: string };
            showKey(api_key, "Refresh to see it applied.");
          });
        }
        break;
      case "delete-user":
        if (userRow && window.confirm("Delete this user? This cannot be undone.")) {
          void send(`/api/admin/users/${userRow.dataset.userId}`, "DELETE").then(reloadOrAlert);
        }
        break;
      case "retry-job":
        if (jobRow) void send(`/api/jobs/${jobRow.dataset.jobId}/retry`, "POST").then(reloadOrAlert);
        break;
      case "restore-item":
        if (itemRow) void send(`/api/admin/items/${itemRow.dataset.itemId}/restore`, "POST").then(reloadOrAlert);
        break;
      case "purge-item":
        if (itemRow) {
          const id = itemRow.dataset.itemId ?? "";
          const title = itemRow.querySelector("td")?.textContent?.trim() || id;
          confirmPurge(id, title);
        }
        break;
      case "test-ingest":
        void testIngest(button.dataset.extractor ?? "");
        break;
    }
  });

  const testResult = root.querySelector<HTMLElement>('[data-role="test-result"]');
  async function testIngest(extractor: string): Promise<void> {
    if (!extractor || !testResult) return;
    const response = await send("/api/admin/test-ingest", "POST", { extractor });
    testResult.hidden = false;
    if (!response.ok) {
      testResult.textContent = `${extractor}: ${await failMessage(response)}`;
      return;
    }
    const { job_id } = (await response.json()) as { job_id: number };
    testResult.textContent = `${extractor}: queued as job ${job_id}. Watch the job queue below.`;
  }

  root.querySelector<HTMLFormElement>('[data-role="create-user"]')?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const data = new FormData(form);
    const username = String(data.get("username") ?? "");
    const response = await send("/api/admin/users", "POST", {
      username,
      display_name: String(data.get("display_name") ?? ""),
      password: String(data.get("password") ?? ""),
      is_admin: data.get("is_admin") === "on",
    });
    if (!response.ok) {
      window.alert(await failMessage(response));
      return;
    }
    const { api_key } = (await response.json()) as { api_key: string };
    form.reset();
    showKey(api_key, `Created "${username}". Refresh to see them listed.`);
  });

  const settingsForm = root.querySelector<HTMLFormElement>('[data-role="settings"]');

  // admin.html (Task 7) renders a row for every entry in db.DefaultSettings,
  // but handleSaveSettings (Task 9) only accepts these six; an unlisted key
  // is rejected outright, before any real field is even looked at. Sending
  // every field in the form would therefore always fail on the first
  // unlisted one. The extra rows are disabled rather than silently dropped
  // from what gets sent (disabled inputs are excluded from FormData below),
  // so it stays visible that they do not save.
  const settableKeys = ["auto_webp", "upload_max_bytes", "upload_chunk_bytes", "download_max_bytes", "ytdlp_format", "cookies_twitter"];
  for (const input of settingsForm?.querySelectorAll<HTMLInputElement>("input[name]") ?? []) {
    if (!settableKeys.includes(input.name)) {
      input.disabled = true;
      input.title = "Not saved by this form yet.";
    }
  }

  function clearSettingsErrors(): void {
    if (!settingsForm) return;
    for (const err of settingsForm.querySelectorAll(".admin__field-err")) err.remove();
    for (const input of settingsForm.querySelectorAll<HTMLInputElement>("input[aria-invalid]")) {
      input.removeAttribute("aria-invalid");
      input.removeAttribute("aria-describedby");
    }
  }

  // Surfaces a rejection next to the field it names, not as a generic
  // banner: the server already names the offending setting at the start of
  // its message (see handleSaveSettings), so matching that prefix against
  // this form's own input names finds the right field without hard-coding
  // the setting list here too.
  function applySettingsError(message: string): void {
    if (!settingsForm) return;
    clearSettingsErrors();
    const lower = message.toLowerCase();
    const input = Array.from(settingsForm.querySelectorAll<HTMLInputElement>("input[name]")).find((node) =>
      lower.startsWith(node.name.toLowerCase()),
    );
    if (!input) {
      window.alert(message);
      return;
    }
    const errId = `${input.name}-err`;
    const err = el("span", "admin__field-err", message);
    err.id = errId;
    err.setAttribute("role", "alert");
    input.setAttribute("aria-invalid", "true");
    input.setAttribute("aria-describedby", errId);
    input.after(err);
  }

  settingsForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const payload: Record<string, string> = {};
    for (const [key, value] of new FormData(settingsForm).entries()) payload[key] = String(value);
    const response = await send("/api/admin/settings", "POST", payload);
    if (response.ok) {
      clearSettingsErrors();
      window.alert("Settings saved.");
    } else {
      applySettingsError(await failMessage(response));
    }
  });
}
