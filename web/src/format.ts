/**
 * Byte and duration formatting shared by client-rendered tiles (grid.ts) and
 * the lightbox metadata panel. Mirrors internal/web/templatefuncs.go's
 * fmtBytes/fmtDuration so a server-rendered tile and one appended later by
 * infinite scroll read identically.
 */

export function formatBytes(n: number): string {
  if (n < 1000) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = n;
  let idx = -1;
  while (value >= 1000 && idx < units.length - 1) {
    value /= 1000;
    idx += 1;
  }
  const unit = units[idx] ?? "TB";
  const rounded = Math.round(value * 10) / 10;
  return Number.isInteger(rounded) ? `${rounded} ${unit}` : `${rounded.toFixed(1)} ${unit}`;
}

export function formatDuration(seconds: number): string {
  const total = Math.max(0, Math.round(seconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const mm = String(m).padStart(2, "0");
  const ss = String(s).padStart(2, "0");
  return h > 0 ? `${h}:${mm}:${ss}` : `${mm}:${ss}`;
}

/** width/height, or 1 (square) when either is unknown: matches aspectRatio
 * in internal/web/templatefuncs.go, the value the justified grid's flex-grow
 * is driven by. */
export function aspectRatio(width: number, height: number): number {
  if (width <= 0 || height <= 0) return 1;
  return width / height;
}
