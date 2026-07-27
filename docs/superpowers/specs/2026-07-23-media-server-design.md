# Media Server Design — boobies-media

Date: 2026-07-23
Status: Approved design, revised after adversarial review (rev 2). Rev 3 (2026-07-24) settles chunked/resumable uploads and the Cloudflare Tunnel exposure model.

## Purpose

A small, fast, self-hosted media server for a private friend group. Friends upload, rename, tag, and organize a large catalog of images, GIFs, webp, and videos. The server ingests media from pasted links (Twitter/X, YouTube, TikTok, Medal.tv, Discord CDN, direct URLs) and produces share links that embed correctly in Discord and Twitter. Inspired by copyparty's speed and small footprint, with a nicer UI. A separate Discord bot (built later, in Bun/TypeScript) consumes the same API; this web application is the source of truth.

## Decisions (settled with user)

| Topic | Decision |
|---|---|
| Foundation | Own server, copyparty as inspiration only |
| Stack | Go single binary (server); Bun builds frontend assets and powers the future bot |
| Auth | Per-friend accounts created by admin; username (tag) + display name + avatar |
| Share links | Unguessable public links per item; catalog browsing stays locked |
| Ingestion sources at launch | Twitter/X (requires cookies — see below), YouTube, TikTok, Medal.tv, Discord CDN, direct media URLs |
| Deployment | User's home server behind own domain, exposed via **Cloudflare Tunnel** (settled rev 3 — see Deployment notes). Origin binds loopback only; `cloudflared` is its sole client. |
| Uploads | **Chunked and resumable** (copyparty/up2k as inspiration, deliberately simpler). Per-chunk size is the cap that matters; total upload cap is an admin setting in GB. |
| Organization | Virtual folders (DB-only tree) + tags; files never move on disk |
| Video | Store original + generate thumbnail; no transcoding. YouTube (and any yt-dlp source) downloads are constrained to H.264 MP4 ≤1080p so Discord embeds play inline. Format string is an admin setting. |
| Image optimization | Admin setting: narrowed lossless webp conversion (8-bit RGB/RGBA static PNG only) |
| Architecture | Server-rendered HTML + small TS islands (approach A) |
| Privacy model | Every item is reachable by its unguessable share link — public-by-obscurity, accepted deliberately for a friend group. A per-item revoke flag exists to kill leaked links. |

## Architecture

One Go process:

- **HTTP server** (chi router)
  - HTML pages rendered from Go templates, assets embedded via `go:embed`
  - JSON API under `/api/*` shared by UI islands and the Discord bot
  - Public unauthenticated routes: `/s/{id}` (embed/viewer page), `/m/{id}` (raw media), `/t/{id}` (thumbnail), login page
- **Job queue**: goroutine workers (default 2) polling a SQLite `jobs` table; shells out to `yt-dlp`, `gallery-dl`, `ffmpeg`/`ffprobe`, `cwebp` (always argv arrays, never shell strings)
- **SQLite** via `modernc.org/sqlite` (pure Go, no cgo)

Bun is build-time only for the web app: bundles TypeScript islands and CSS, output embedded into the Go binary. Result: single deployable binary + external tool dependencies.

### Startup checks

- **Hard fail** (exit with plain-English message): data dir not writable, DB unopenable.
- **Soft fail** (boot anyway, persistent admin-page banner, relevant jobs fail with clear errors): yt-dlp, gallery-dl, ffmpeg, cwebp missing. A home server must still serve the existing library when a distro upgrade breaks a tool. Admin page shows `yt-dlp --version` (staleness is the #1 cause of ingest breakage).
- On startup, recover crashed jobs: `UPDATE jobs SET status='queued' WHERE status='running'`.

### Disk layout

```
data/
  media.db                     # SQLite, all metadata
  backups/media-<date>.db      # nightly VACUUM INTO, keep 7
  files/ab/cd/<hash>           # originals, SHA-256 content-addressed, sharded
  files/ab/cd/<hash>.json      # sidecar: {ext,mime,title,source_url,uploader} so blobs self-describe if DB is lost
  thumbs/ab/cd/<hash>_320.webp # thumbnail variants
  thumbs/ab/cd/<hash>_1024.webp
  avatars/<avatar_hash>.webp   # content-hash named for cache-busting
  cookies/                     # optional per-extractor cookie files (twitter.txt etc.)
```

Content-addressed storage: duplicate uploads dedup to one file; rename/move is a DB row update; media responses use immutable cache headers.

### SQLite configuration

DSN pragmas: `busy_timeout(5000)`, `journal_mode(WAL)`, `synchronous(NORMAL)`, `foreign_keys(1)`. Single write pool with `SetMaxOpenConns(1)` (plus a read pool if ever needed — at friends-scale one connection is fine and eliminates `SQLITE_BUSY` as a class).

Backups: nightly `VACUUM INTO data/backups/media-<date>.db` (safe on live WAL), retain 7.

## Data model (SQLite)

- `users`: id, username (unique tag), display_name, avatar_hash, password_hash (argon2id), api_key_hash (SHA-256 of the key; plaintext shown once at creation), is_admin, created_at
- `items`: id (random 8-char base58, `UNIQUE` with retry-on-conflict insert; doubles as public share slug), content_hash, title, ext, mime, size, width, height, duration, uploader_id, folder_id, source_url, job_id (nullable — links item to the ingest job that produced it), share_revoked (bool, default false), deleted_at (nullable — soft delete), created_at
- `folders`: id, parent_id, name; `UNIQUE(parent_id, name)`. Folder move validates the target is not the folder itself or a descendant (walk ancestors, reject cycles).
- `tags`: id, name (unique, lowercased); `item_tags`: item_id, tag_id
- `jobs`: id, type (`ingest_url` | `thumbnail` | `probe`), payload JSON, status (`queued`/`running`/`done`/`failed`), attempts, next_attempt_at, error, created_at
- `sessions`: token_hash (SHA-256; cookie holds plaintext), user_id, expires_at
- `uploads`: id (random token, the upload handle), user_id, folder_id, filename, declared_size, chunk_size, received JSON (indices already stored), temp_dir, created_at, expires_at. Rows are transient — a janitor reaps expired ones and their temp bytes.
- `settings`: key, value (`auto_webp`, `upload_max_bytes` (GB-scale total cap), `upload_chunk_bytes`, yt-dlp format string, download size cap)

Indexes: `items(content_hash)`, `items(folder_id, created_at)`, `items(uploader_id)`, `items(created_at)`, `item_tags(tag_id, item_id)` (plus PK on `(item_id, tag_id)`).

Albums/collections: cut from v1 (YAGNI); folders + tags cover organization.

## Auth, authorization, sharing

- Login: username + password, argon2id, session cookie (HttpOnly, Secure, SameSite=Lax, 30-day). Rate-limited. No self-signup. Password change invalidates existing sessions. The `Secure` flag comes from **config**, never from `r.TLS` — behind the tunnel the origin always sees plain HTTP over loopback even though the browser is on HTTPS.
- **Client IP** has exactly one source, `clientIP()`: `CF-Connecting-IP` first, `RemoteAddr` as fallback. It is trustworthy because the origin binds loopback and `cloudflared` is the only peer that can reach it. Arbitrary `X-Forwarded-For` is never trusted. (This closes the trusted-proxy follow-up deferred out of Plan 1.)
- API auth: per-user key via `Authorization: Bearer`; DB stores only its hash; rotate to revoke.
- **Delete authorization**: uploader-or-admin only. Delete is soft (`deleted_at`); admin purge actually removes. Purge unlinks the blob **only when no other non-deleted item references the same `content_hash`** (refcount check — two friends uploading the same meme share one file).
- Public surface is exactly `/s/{id}`, `/m/{id}`, `/t/{id}`, login. Share IDs random 8-char base58 (~2^47). `share_revoked` makes all three routes 404 for that item without deleting it.
- Per-IP token-bucket rate limit on `/m/` and `/t/`.

### Media serving (`/m/{id}`, `/t/{id}`) — security critical

- **Served-mime allowlist**: `image/jpeg`, `image/png`, `image/gif`, `image/webp`, `image/avif`, `video/mp4`, `video/webm`. Files whose sniffed type is outside the allowlist are **rejected at ingest** (SVG and HTML explicitly rejected — served same-origin they are stored XSS against the session cookie).
- Response headers: `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src 'none'; sandbox`, `Content-Disposition: inline` with sanitized filename, immutable cache headers. Content-Type comes from the stored allowlisted mime, never re-sniffed at serve time.
- **HTTP Range support required**: serve via `http.ServeContent` with a real `io.ReadSeeker`. Safari refuses `<video>` without 206 responses; Discord's proxy range-requests too.
- Download size caps enforced (`--max-filesize` for yt-dlp, Content-Length/read cap on direct fetches, free-disk precheck).

### Embed page `/s/{id}`

**One document for all clients** — no crawler UA sniffing. OG/Twitter tags live in `<head>`; the human viewer (media, title, uploader avatar + name, source link, copy button) lives in `<body>`. Crawlers read the head; humans get the page. `Referrer-Policy: no-referrer` so share ids don't leak via the source-link anchor.

Exact tag sets (all URLs absolute):

- Image items: `og:type=website`, `og:title`, `og:url`, `og:image` (+ `og:image:width/height/type`), `twitter:card=summary_large_image`.
- Video items: `og:type=video.other`, `og:title`, `og:url`, `og:video:secure_url` (https), `og:video:type`, `og:video:width`, `og:video:height`, **plus** `og:image` poster, `twitter:card=summary_large_image`.
- Twitter/X inline video playback is out of scope (Player Card needs an approved domain); Twitter gets the poster-image card. Discord is the target.
- Non-H.264 video (e.g. pre-existing webm): falls back to image-card embed with poster; the item page still plays it where the browser can.

Golden-file tests assert **every** tag per case, not merely that markup exists.

## Ingestion pipeline

Entry points:

1. **File upload** — drag-and-drop or picker, multiple files, **chunked and resumable**. copyparty's up2k is the inspiration, not the target: this is chunk + offset + resume, nothing more. No client-side dedup handshake, no parallel multi-file swarm.

   - **Protocol.** `POST /api/uploads` declares `{filename, size, folder_id}` and returns `{upload_id, chunk_size, received: []}`. Each chunk goes to `PUT /api/uploads/{id}/{index}` (raw body, no multipart). `GET /api/uploads/{id}` returns the indices already stored, so an interrupted client resumes by uploading only the gaps. `POST /api/uploads/{id}/complete` assembles and returns the item. Chunk writes are idempotent — re-sending a chunk is always safe.
   - **The per-chunk size is the cap that matters, not the file size.** Default chunk **12 MB**, admin-tunable via `upload_chunk_bytes`. It must stay under Cloudflare's 100 MB request-body cap and must finish inside the 125 s proxy read timeout at the server's worst upstream — 12 MB clears both with headroom.
   - **Total upload cap is now an admin setting in GB** (`upload_max_bytes`), enforced against the declared size at `POST /api/uploads` and again against bytes actually received. The old 95 MB default is gone; it existed only to fit under Cloudflare's per-request cap, which chunking removes as a constraint.
   - Chunks land in a per-upload temp dir and are assembled on completion, then enter the normal per-file pipeline (sniff → allowlist → webp → hash → move). Nothing is content-addressed until assembly finishes.
   - A **janitor reaps abandoned uploads** (row + temp dir) past `expires_at`, so a friend closing a laptop mid-upload cannot leak disk forever.
   - Upload endpoints are session-authenticated **and** origin-checked (`Sec-Fetch-Site`/`Origin`, plus a CSRF token in the island's fetches) — this closes the CSRF follow-up Plan 1's review deferred until uploads existed.
2. **URL paste** — creates an `ingest_url` job; the response and UI track the **job** (an item row cannot exist before download completes and the hash is known — the uploader island binds to job status, and finished jobs return their item ids/share URLs via `job_id` linkage). Classification:
   - direct media URL → guarded HTTP fetch
   - Discord CDN (`cdn.discordapp.com`, `media.discordapp.net`) → guarded fetch immediately (URLs expire)
   - Twitter/X, YouTube, TikTok, Medal.tv → `yt-dlp` with `--no-playlist`, format string from settings (default `bv*[vcodec^=avc1][height<=1080]+ba[acodec^=mp4a]/b[ext=mp4]/b`, merged to mp4); `gallery-dl` fallback for Twitter image galleries. One job may yield N items (galleries).
   - `source_url` stored on resulting item(s).

**SSRF guard** on all direct fetches: resolve hostname first; reject private/loopback/link-local/CGNAT ranges; `http`/`https` only; redirects capped and re-validated per hop via `CheckRedirect`; response size capped.

**Twitter/X reality**: unauthenticated access is gone; yt-dlp and gallery-dl need an exported cookie file. Admin settings hold per-extractor cookie paths (`data/cookies/twitter.txt` → `--cookies`); admin page has a per-source "test ingest" button so breakage is visible, not silent. Cookie re-export is a known recurring maintenance cost.

Per-file pipeline: sniff mime from magic bytes (never trust extension) → **reject if outside served-mime allowlist** → webp auto-optimize step (below) → SHA-256 → move into `files/` + write sidecar JSON → enqueue probe/thumbnail job (ffprobe dimensions/duration; ffmpeg poster frame for video; thumbnails at 320px and 1024px, route `/t/{id}?s=320|1024` with strict allowlist on `s`).

### Webp auto-optimization (admin setting, narrowed scope)

- Applies **only** to static 8-bit RGB/RGBA PNG. APNG is detected via the `acTL` chunk and skipped (cwebp would silently keep frame 1 only). 16-bit, palette-with-transparency edge cases, BMP, and TIFF are all excluded — outside this narrow scope "lossless" cannot be guaranteed.
- `cwebp -lossless -metadata all` (preserves EXIF/ICC). Keep the conversion only if output is smaller; any non-zero exit, oversize (>16383px), or failure → keep original silently (not a job failure).
- JPEG excluded (lossless webp of JPEG is typically larger; lossy re-encode violates the constraint). Animated GIF excluded.
- Runs before hashing, so dedup operates on optimized bytes. Known caveat: toggling the setting (or a libwebp upgrade) changes the bytes the same source hashes to, so dedup does not connect across that boundary. Accepted at friends-scale.

### Job handling

Workers poll `WHERE status='queued' AND next_attempt_at <= now`. Retry with backoff via `next_attempt_at`, 3 attempts, error string surfaced in UI with a retry button.

## UI

Dark-first, thumbnail-dense. Pages:

- **Login**
- **Browse**: folder tree sidebar, tag filter chips, search box; justified thumbnail grid with infinite scroll using **keyset pagination** (`WHERE (created_at, id) < (?, ?)` — offset pagination duplicates/skips as friends upload); sort by date/name/size/uploader
- **Item view** (lightbox): media, inline title rename, tag editor, folder move, source link, uploader attribution, copy-share-link, revoke-share, delete (soft)
- **Upload**: global drag-drop + paste-URL box; URL ingests show job progress, then resulting items
- **Admin**: users CRUD, settings (webp toggle, caps, yt-dlp format, cookie paths), job queue status, dependency banner, trash (soft-deleted items, restore/purge)

Islands (TypeScript, Bun-bundled; vanilla or at most Preact-sized): uploader, grid virtualizer, lightbox, tag editor, bulk-select toolbar. JS budget < 50 KB.

## Bot-facing API

Same `/api/*` surface the islands use, Bearer key:

- `GET /api/items?query=&tag=&folder=&uploader=&sort=&cursor=`
- `GET /api/items/{id}` — metadata + share URL
- `GET /api/random?tag=`
- `POST /api/ingest` — JSON `{url}` or multipart file → job id. The multipart branch stays a single-shot convenience for bots posting small files and is capped at `upload_chunk_bytes`; anything larger uses the chunked `/api/uploads` flow the browser island uses.
- `POST /api/uploads`, `PUT /api/uploads/{id}/{index}`, `GET /api/uploads/{id}`, `POST /api/uploads/{id}/complete` — chunked resumable upload (see Ingestion pipeline)
- `GET /api/jobs/{id}` — status **plus resulting item ids/share URLs when done**

## Deployment notes (part of the design, not an afterthought)

### Exposure model: Cloudflare Tunnel (settled, rev 3)

`cloudflared` runs on the home server; the Go origin binds `127.0.0.1` only. No port forwarding, no public home IP, TLS terminated at the Cloudflare edge.

These are code constraints, not just runbook steps:

- **Every route is proxied.** A tunnel has no unproxied path, so there is no "bypass Cloudflare for the upload route" escape hatch. The **100 MB request-body cap** (Free/Pro; `413` above it) applies to every request the server will ever see.
- **That cap is per request, not per file** — which is precisely why uploads are chunked. A 12 MB chunk is a 12 MB body; total file size is unbounded by Cloudflare. Chunking and the tunnel are complementary rather than in tension: chunking is what removes the cap as a user-visible limit. The earlier "media/upload routes not proxied through CF" revision is **superseded** — its only job was to raise the body cap, and chunking does that without giving up the tunnel.
- **Proxy read timeout is 125 s**, tunable on Enterprise only. Chunk size must be small enough that one chunk uploads within it at the server's worst upstream.
- **Client IP** is trustworthy here for a structural reason: loopback bind means `cloudflared` is the only peer that can reach the origin, so `CF-Connecting-IP` cannot be spoofed by a remote client. See the auth section — `clientIP()` remains the single source.
- **Cookie `Secure` flag from config, never `r.TLS`** — the origin sees plain HTTP regardless of what the browser sees.

### Cloudflare dashboard configuration (required, not optional)

- WAF / Bot Fight Mode must **skip crawler user agents on `/s/*`, `/m/*`, `/t/*`**. A challenged Discordbot caches a failed embed. With a tunnel this is mandatory — there is no DNS-only path to fall back to.
- **Cache Rule: bypass cache on `/m/*` and `/t/*`.** Keeps the media library out of Cloudflare's CDN, which is the behavior self-serve ToS §2.8 actually restricts (disproportionate non-HTML/video caching). It also avoids serving stale bytes after a purge.
- Nightly backup timer ships in the binary (no external cron needed).

### If the tunnel is ever replaced by a direct reverse proxy

The trust story inverts: `CF-Connecting-IP` stops being authoritative and the proxy must be pinned explicitly by address. Keep the client-IP source behind the one `clientIP()` function so that is a single-file change. Also note nginx's `client_max_body_size` defaults to 1 MB and will reject chunks outright.

## Error handling

- API: JSON `{error, code}` + proper status. HTML: friendly error page.
- Job failures: per-item/job status + retry.
- Startup: hard/soft split per Startup checks section.

## Testing

- Table tests: auth flows, access-control boundary, URL classifier, webp-policy decisions (including APNG skip), folder-cycle rejection, delete refcounting.
- Integration: real HTTP server + temp SQLite + temp data dir; upload → probe → thumbnail → share-page round trip; **chunked-upload resume test** (send chunks 0 and 2, re-`GET` the upload to confirm the server reports the gap, send chunk 1, complete, assert the assembled bytes hash to the original); **oversize-declaration test** (declared size above `upload_max_bytes` rejected at init, and a client lying about its size rejected at completion); **Range test** (`Accept-Ranges: bytes`, 206 on `Range: bytes=0-1023`); **dedup-delete test** (upload same bytes twice, purge one, other still serves); **mime-allowlist test** (SVG/HTML upload rejected).
- Golden-file tests for embed markup, every tag asserted per image/video case.
- External ingestion mocked (stub yt-dlp binary); no network in tests.

## Out of scope for v1

Albums, transcoding, animated-GIF conversion, chunked/resumable uploads, self-signup, Twitter inline video playback, the Discord bot itself (separate project consuming this API).
