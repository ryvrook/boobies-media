/**
 * YouTube-style hover-to-play previews for video and GIF tiles in the browse
 * grid: a still poster at rest, then inline playback after a short,
 * cancellable hover-intent delay, restoring the still on leave.
 *
 * Works uniformly on both server-rendered tiles (present in the DOM before
 * this script runs) and tiles appended later by grid.ts's renderTile() during
 * infinite scroll: both carry the same data-mime / data-media-url attributes
 * and the same "tile--processing" class for an item with no thumbnail yet.
 */

// 400-600ms is the requested band. 500ms sits in the middle and matches the
// common "hover intent" heuristic (as used by e.g. menu/tooltip libraries)
// for telling a deliberate pause from a cursor merely sweeping across a tile
// on its way somewhere else.
const HOVER_INTENT_DELAY_MS = 500;

const VIDEO_MIMES = new Set(["video/mp4", "video/webm"]);

interface ActivePreview {
  article: HTMLElement;
  stop: () => void;
}

// Only one preview ever plays at a time, across the whole grid.
let active: ActivePreview | null = null;

function prefersReducedMotion(): boolean {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

/** Stops whatever preview is currently playing, if any. */
export function stopActivePreview(): void {
  if (!active) return;
  const current = active;
  active = null;
  current.stop();
}

/**
 * Attaches hover/focus-intent preview behaviour to one grid tile. A no-op for
 * a tile with no thumbnail yet, or for an item that is neither a video nor a
 * GIF (a plain still has nothing to preview).
 */
export function mountTilePreview(article: HTMLElement): void {
  if (article.classList.contains("tile--processing")) return;
  const mime = article.dataset.mime ?? "";
  const mediaUrl = article.dataset.mediaUrl;
  const isVideo = VIDEO_MIMES.has(mime);
  const isGif = mime === "image/gif";
  if (!mediaUrl || (!isVideo && !isGif)) return;

  const button = article.querySelector<HTMLElement>(".tile__button");
  const poster = article.querySelector<HTMLImageElement>(".tile__image");
  if (!button || !poster) return;

  // A video plays muted/looped/inline with no controls, exactly like a GIF
  // would if a browser let you script one. That is exactly why GIFs get
  // this same treatment instead of an <img> that starts animating the moment
  // its src loads and can never be paused.
  const preview: HTMLVideoElement | HTMLImageElement = isVideo
    ? document.createElement("video")
    : document.createElement("img");
  preview.className = "tile__preview";
  preview.setAttribute("aria-hidden", "true");
  preview.tabIndex = -1;
  preview.hidden = true;
  if (preview instanceof HTMLVideoElement) {
    preview.muted = true;
    preview.loop = true;
    preview.playsInline = true;
    // No src is set at all until hover intent fires (see start()): a grid
    // of 60 items must not fetch 60 videos just because they were rendered.
    preview.preload = "none";
  } else {
    preview.alt = "";
  }
  button.appendChild(preview);

  let timer: ReturnType<typeof setTimeout> | null = null;

  function cancelPending(): void {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  }

  function stop(): void {
    cancelPending();
    preview.hidden = true;
    if (preview instanceof HTMLVideoElement) {
      preview.pause();
      // Removing the attribute and reloading aborts any in-flight fetch
      // instead of letting an abandoned preview finish downloading unseen.
      preview.removeAttribute("src");
      preview.load();
    } else {
      preview.removeAttribute("src");
    }
  }

  function start(): void {
    if (prefersReducedMotion()) return; // never autoplay motion on hover
    if (active && active.article !== article) stopActivePreview();
    // Narrowed to a definite string by the guard at the top of
    // mountTilePreview; TypeScript does not carry that narrowing into this
    // nested closure, but mediaUrl is a const and cannot have changed.
    preview.src = mediaUrl!;
    preview.hidden = false;
    if (preview instanceof HTMLVideoElement) {
      preview.play().catch(() => {
        // Autoplay can still be refused by policy even when muted. The still
        // poster underneath is already showing, so there is nothing to
        // recover: just leave it rather than surface an error for this.
      });
    }
    active = { article, stop };
  }

  function schedule(): void {
    cancelPending();
    // Multi-select mode (see bulkselect.ts) is for picking tiles, not
    // watching them; a preview starting under the checkboxes while the user
    // is trying to select would be a distraction at best.
    if (document.body.classList.contains("selecting")) return;
    timer = setTimeout(start, HOVER_INTENT_DELAY_MS);
  }

  function leave(): void {
    cancelPending();
    if (active?.article === article) {
      active = null;
      stop();
    }
  }

  // Mouse hover only: a stylus hovering close to a screen can also report
  // pointerType "pen" without the deliberate intent a mouse hover implies,
  // and touch has no hover concept at all. Starting a preview fetch on a tap
  // would be surprising, and wasteful on mobile data, right before that same
  // tap opens the lightbox. Tap-to-open is unaffected: it is wired
  // separately, by delegated click handling in lightbox.ts.
  button.addEventListener("pointerenter", (event: PointerEvent) => {
    if (event.pointerType !== "mouse") return;
    schedule();
  });
  button.addEventListener("pointerleave", (event: PointerEvent) => {
    if (event.pointerType !== "mouse") return;
    leave();
  });
  // Keyboard focus is the equivalent trigger for non-pointer users, so the
  // preview is not mouse-only.
  button.addEventListener("focus", schedule);
  button.addEventListener("blur", leave);
}
