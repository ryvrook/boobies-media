/**
 * Tests for the two ways a request fails.
 *
 * The interesting case is a *rejecting* fetch, not a 500. A non-OK response
 * is easy to notice and every call site already looked at it; a rejection
 * (offline, dropped connection, DNS, server killed mid-request) skips the
 * failure branch entirely, so an unguarded call site shows no error and never
 * rolls its control back. These tests pin that behaviour down.
 *
 * They stub globalThis.fetch, so nothing here touches the network.
 *
 * Run with `bun test`. This file is excluded from tsconfig.json on purpose:
 * type-checking `bun:test` would mean adding a dependency to a project that
 * deliberately has none, and bun runs it without needing one.
 */

import { afterEach, describe, expect, test } from "bun:test";

import { apiSend, UNREACHABLE, UNREADABLE } from "./request";

const realFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = realFetch;
});

/** Replaces fetch with one that rejects, the way an offline browser does. */
function fetchRejects(reason = "Failed to fetch"): void {
  globalThis.fetch = (() => Promise.reject(new TypeError(reason))) as typeof fetch;
}

/** Replaces fetch with one that resolves to the given response. */
function fetchResolves(status: number, body?: unknown, raw?: string): void {
  globalThis.fetch = (() =>
    Promise.resolve(
      new Response(raw ?? (body === undefined ? null : JSON.stringify(body)), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    )) as typeof fetch;
}

describe("a rejecting fetch", () => {
  test("is reported as a failure with a message, not thrown", async () => {
    fetchRejects();
    const result = await apiSend("/api/items/abc", "PATCH", { folder_id: 3 });
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("unreachable");
    expect(result.message).toBe(UNREACHABLE);
    // status 0 is what distinguishes "no response at all" from any HTTP code,
    // so a call site keying on 403 cannot accidentally match it.
    expect(result.status).toBe(0);
  });

  test("fails the same way for every verb the islands use", async () => {
    for (const method of ["GET", "POST", "PATCH", "DELETE"]) {
      fetchRejects();
      const result = await apiSend(`/api/folders`, method, method === "GET" ? undefined : { name: "x" });
      expect(result.ok).toBe(false);
      if (!result.ok) expect(result.message).toBe(UNREACHABLE);
    }
  });

  test("never reports success, so no caller can commit state on a dropped request", async () => {
    fetchRejects();
    const result = await apiSend<{ item: { folder_id: number } }>("/api/items/abc", "PATCH", { folder_id: 9 });
    expect(result.ok).toBe(false);
    expect("body" in result).toBe(false);
  });
});

describe("a non-OK response", () => {
  test("prefers the server's own message", async () => {
    fetchResolves(409, { code: "duplicate_folder", error: "a folder with that name already exists here" });
    const result = await apiSend("/api/folders", "POST", { name: "Memes" }, "fallback");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("unreachable");
    expect(result.message).toBe("a folder with that name already exists here");
    expect(result.status).toBe(409);
  });

  test("falls back when the body carries no message", async () => {
    fetchResolves(500, undefined, "not json at all");
    const result = await apiSend("/api/items/abc", "PATCH", { title: "x" }, "That rename did not save.");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("unreachable");
    expect(result.message).toBe("That rename did not save.");
  });

  test("keeps the status so a caller can word 403 itself", async () => {
    fetchResolves(403, { error: "forbidden" });
    const result = await apiSend("/api/items/abc", "DELETE");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("unreachable");
    expect(result.status).toBe(403);
  });
});

describe("a successful response", () => {
  test("decodes the body", async () => {
    fetchResolves(200, { item: { id: "abc", folder_id: 3 } });
    const result = await apiSend<{ item: { id: string; folder_id: number } }>("/api/items/abc", "GET");
    expect(result.ok).toBe(true);
    if (!result.ok) throw new Error("unreachable");
    expect(result.body.item.folder_id).toBe(3);
  });

  test("treats 204 as success with nothing to decode", async () => {
    fetchResolves(204);
    const result = await apiSend<void>("/api/folders/1", "DELETE");
    expect(result.ok).toBe(true);
  });

  test("treats an undecodable 2xx as a failure, since there is nothing to update from", async () => {
    fetchResolves(200, undefined, "<html>a proxy error page</html>");
    const result = await apiSend("/api/items/abc", "PATCH", { folder_id: 1 });
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("unreachable");
    expect(result.message).toBe(UNREADABLE);
  });
});

/**
 * The guarantee above is only worth anything if every call site actually goes
 * through it. These two islands were audited and converted; this fails the
 * moment someone reintroduces a bare fetch into either, which is exactly how
 * the original gap appeared.
 */
describe("the islands that were audited", () => {
  const guarded = ["web/src/islands/lightbox.ts", "web/src/islands/folders.ts"];

  for (const path of guarded) {
    test(`${path} calls no fetch directly`, async () => {
      const source = await Bun.file(path).text();
      expect(source).not.toMatch(/[^.\w]fetch\s*\(/);
      expect(source).toContain("apiSend");
    });
  }
});
