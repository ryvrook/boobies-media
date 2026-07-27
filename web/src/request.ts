/**
 * One place where a call to this server's JSON API becomes either a decoded
 * body or a message fit to show a user.
 *
 * It exists because `fetch` fails in two entirely different ways and only one
 * of them is a response. A 404 or a 409 resolves, carries a status and a
 * JSON `error` string, and is easy to notice. Losing the network, the tunnel
 * or the server mid-request *rejects* instead, and a call site that only
 * inspects `response.ok` never runs at all: no error is shown, and any
 * "put the control back" logic sitting in the failure branch is skipped too.
 * A control then keeps displaying a change that was never saved, which is a
 * worse outcome than a visible failure. Routing both failures to one shape
 * makes that impossible to get wrong by omission.
 *
 * Deliberately DOM-free and island-free, so both the browse page's folder
 * management and the item viewer can use it, and so it can be tested without
 * a browser or a network.
 */

/**
 * A failed call always carries a message ready to display. `status` is the
 * HTTP status, or 0 when the request never produced a response at all.
 */
export type ApiResult<T> =
  | { ok: true; body: T }
  | { ok: false; status: number; message: string };

/** Shown when fetch rejects: there was no response to read a message from. */
export const UNREACHABLE = "The server could not be reached.";

/** Shown when a 2xx response body is not the JSON the caller expected. */
export const UNREADABLE = "The server sent a reply this page could not read.";

/**
 * Sends a request and decodes its JSON body.
 *
 * `fallback` is used only when the server sent no `error` of its own; the
 * server's own wording is always preferred, because it is the one that knows
 * whether the name was a duplicate or the move was a cycle.
 *
 * A 204 resolves to `undefined`; ask for `ApiResult<void>` at those sites.
 */
export async function apiSend<T>(
  url: string,
  method: string,
  body?: unknown,
  fallback?: string,
): Promise<ApiResult<T>> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";

  let response: Response;
  try {
    response = await fetch(url, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    return { ok: false, status: 0, message: UNREACHABLE };
  }

  if (!response.ok) {
    const detail = (await response.json().catch(() => null)) as { error?: string } | null;
    return {
      ok: false,
      status: response.status,
      message: detail?.error ?? fallback ?? `That did not work (${response.status}).`,
    };
  }

  if (response.status === 204) return { ok: true, body: undefined as T };

  // A 2xx this page cannot decode is still a failure from the user's side:
  // the caller has nothing to update from, so it must not report success.
  const decoded = (await response.json().catch(() => null)) as T | null;
  if (decoded === null) return { ok: false, status: response.status, message: UNREADABLE };
  return { ok: true, body: decoded };
}
