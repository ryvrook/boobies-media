import { describe, expect, test } from "bun:test";

import { pollItemUntilReady } from "./processing";
import type { ApiItem } from "./islands/grid";

function item(ready: boolean): ApiItem {
  return {
    id: "media id/1",
    title: "bird",
    mime: "image/png",
    ext: "png",
    size: 10,
    width: ready ? 640 : 0,
    height: ready ? 480 : 0,
    duration: 0,
    uploader: 1,
    folder_id: 0,
    is_video: false,
    is_gif: false,
    ready,
    revoked: false,
    created_at: "2026-07-27T00:00:00Z",
    share_url: "",
    media_url: "",
    thumb_url: "",
    source_url: "",
    tags: [],
  };
}

function response(value: ApiItem, status = 200): Response {
  return new Response(JSON.stringify({ item: value }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const noWait = async (): Promise<void> => {};

describe("pollItemUntilReady", () => {
  test("returns the refreshed item when processing finishes", async () => {
    const states = [item(false), item(true)];
    const paths: string[] = [];
    const fetcher = (async (path: string | URL | Request) => {
      paths.push(String(path));
      return response(states.shift()!);
    }) as typeof fetch;

    const result = await pollItemUntilReady("media id/1", { fetcher, wait: noWait, attempts: 3 });

    expect(result?.ready).toBe(true);
    expect(paths).toEqual(["/api/items/media%20id%2F1", "/api/items/media%20id%2F1"]);
  });

  test("retries transient request failures", async () => {
    let calls = 0;
    const fetcher = (async () => {
      calls++;
      if (calls === 1) throw new TypeError("offline");
      if (calls === 2) return new Response(null, { status: 503 });
      return response(item(true));
    }) as typeof fetch;

    const result = await pollItemUntilReady("abc", { fetcher, wait: noWait, attempts: 3 });

    expect(result?.ready).toBe(true);
    expect(calls).toBe(3);
  });

  test("stops after the configured attempt cap", async () => {
    let calls = 0;
    const fetcher = (async () => {
      calls++;
      return response(item(false));
    }) as typeof fetch;

    const result = await pollItemUntilReady("abc", { fetcher, wait: noWait, attempts: 2 });

    expect(result).toBeNull();
    expect(calls).toBe(2);
  });
});
