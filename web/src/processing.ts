import type { ApiItem } from "./islands/grid";

interface ItemResponse {
  item: ApiItem;
}

interface PollOptions {
  fetcher?: typeof fetch;
  wait?: (milliseconds: number) => Promise<void>;
  attempts?: number;
  interval?: number;
}

const DEFAULT_ATTEMPTS = 400;
const DEFAULT_INTERVAL = 1500;

/**
 * Polls a newly-created item until the background probe marks it ready.
 *
 * A failed request is deliberately treated as transient: a brief network
 * interruption should not strand the tile in its processing state. The
 * attempt cap keeps a permanently failed probe from polling forever.
 */
export async function pollItemUntilReady(itemID: string, options: PollOptions = {}): Promise<ApiItem | null> {
  const fetcher = options.fetcher ?? fetch;
  const wait = options.wait ?? ((milliseconds) => new Promise((resolve) => window.setTimeout(resolve, milliseconds)));
  const attempts = options.attempts ?? DEFAULT_ATTEMPTS;
  const interval = options.interval ?? DEFAULT_INTERVAL;

  for (let attempt = 0; attempt < attempts; attempt++) {
    await wait(interval);
    try {
      const response = await fetcher(`/api/items/${encodeURIComponent(itemID)}`, {
        headers: { Accept: "application/json" },
      });
      if (!response.ok) continue;
      const body = (await response.json()) as ItemResponse;
      if (body.item.ready) return body.item;
    } catch {
      // Offline, a proxy restart, or a dropped request is retryable.
    }
  }
  return null;
}
