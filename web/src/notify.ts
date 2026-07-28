export type NoticeTone = "success" | "error" | "info";

const FLASH_KEY = "boobies-media:flash";

interface FlashNotice {
  message: string;
  tone: NoticeTone;
}

function region(): HTMLElement {
  let node = document.querySelector<HTMLElement>('[data-role="notifications"]');
  if (node) return node;
  node = document.createElement("div");
  node.className = "notifications";
  node.dataset.role = "notifications";
  node.setAttribute("aria-live", "polite");
  node.setAttribute("aria-atomic", "false");
  document.body.appendChild(node);
  return node;
}

export function notify(message: string, tone: NoticeTone = "success"): void {
  const notice = document.createElement("div");
  notice.className = `notice notice--${tone}`;
  notice.setAttribute("role", tone === "error" ? "alert" : "status");

  const text = document.createElement("span");
  text.textContent = message;
  const close = document.createElement("button");
  close.type = "button";
  close.className = "notice__close";
  close.setAttribute("aria-label", "Dismiss notification");
  close.textContent = "×";
  close.addEventListener("click", () => notice.remove());
  notice.append(text, close);
  region().appendChild(notice);

  window.setTimeout(() => {
    notice.classList.add("notice--leaving");
    window.setTimeout(() => notice.remove(), 180);
  }, tone === "error" ? 7000 : 4000);
}

/** Carries a success message across actions that intentionally reload. */
export function notifyAfterReload(message: string, tone: NoticeTone = "success"): void {
  try {
    window.sessionStorage.setItem(FLASH_KEY, JSON.stringify({ message, tone } satisfies FlashNotice));
  } catch {
    // The action still succeeded if private storage is unavailable.
  }
}

export function showPendingNotification(): void {
  try {
    const raw = window.sessionStorage.getItem(FLASH_KEY);
    if (!raw) return;
    window.sessionStorage.removeItem(FLASH_KEY);
    const flash = JSON.parse(raw) as FlashNotice;
    if (flash.message) notify(flash.message, flash.tone);
  } catch {
    // A malformed or unavailable storage entry should never break the app.
  }
}
