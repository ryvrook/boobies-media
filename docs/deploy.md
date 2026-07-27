# Deploying boobies-media

The server is a single Go binary plus external tools (`yt-dlp`, `gallery-dl`,
`ffmpeg`, `ffprobe`, `cwebp`). It serves HTTP on `-addr` and keeps all state
under `-data`. Put a reverse proxy in front for TLS; a Cloudflare Tunnel is the
supported setup and is what the rest of this document assumes.

## Build

```bash
bun install
bun run build          # bundles web/src into web/static/dist (embedded by go:embed)
go build -o bin/server ./cmd/server
```

## First run

There is no self-signup: `server user add` is the only way an account is
created, so the first account must be created with `--admin` or nobody can
reach `/admin` afterwards.

```bash
./bin/server user add aiden --display-name "Aiden" --admin   # prompts for a password
./bin/server -addr 127.0.0.1:8080 -data /srv/media -base-url https://media.example.com
```

`user add` prints the new account's API key once, to stdout; store it, it is
not shown again and is not recoverable from the database (only its hash is
kept).

`-base-url` must be the public HTTPS origin: it is what absolute links and
OpenGraph tags are built from, so any client that fetches media over a URL
this server hands out (including Discord's embed fetcher, once the `/s/{id}`
embed page lands) gets the right host. Getting it wrong means broken links.

## Upload size limits and why uploads are chunked

Cloudflare proxies request bodies only up to **100 MB** on Free and Pro plans
(200 MB Business, 500 MB Enterprise), and answers `413` above that. A
Cloudflare Tunnel is proxied by definition, so there is no unproxied route to
fall back on.

Uploads are therefore chunked. Two admin-editable settings govern this,
saved through the admin page (`POST /api/admin/settings`, in
`internal/web/handlers_admin_settings.go`):

- `upload_chunk_bytes`, default **12 MiB** (12582912 bytes). One chunk is one
  HTTP request, so this is the only value that has to respect Cloudflare's
  cap. The save endpoint enforces **1 MiB to 64 MiB** on this setting
  specifically; 64 MiB leaves a 36 MiB margin under the 100 MB proxy cap for
  multipart and header overhead.
- `upload_max_bytes`, default **8 GiB** (8589934592 bytes). This is a policy
  limit the admin picks, not an infrastructure one. Raising it needs no proxy
  change at all, because it is enforced only against the running total of a
  chunked upload, never against a single request body.
- `download_max_bytes` (for remote ingests, not uploads) defaults to **2 GiB**
  (2147483648 bytes) and is not proxied through the tunnel at all, since the
  server fetches it directly.

The number to watch when something breaks is the chunk size, not the file
size.

Two further constraints on the chunk size:

- Cloudflare's proxy read timeout is **125 s** and is tunable on Enterprise
  only. A chunk must upload within it on the server's worst upstream. At
  1 Mbit/s upstream, 12 MiB takes about 100 s; if the connection is slower
  than that, lower `upload_chunk_bytes` rather than trying to raise the
  timeout, which you cannot do below Enterprise.
- If a plain reverse proxy is ever put in front instead of the tunnel,
  **nginx** defaults `client_max_body_size` to **1 MB** and will reject every
  chunk until raised above `upload_chunk_bytes`. **Caddy** has no low default:

  ```
  media.example.com {
      reverse_proxy 127.0.0.1:8080
  }
  ```

Saving a chunk/max pair that would leave `upload_max_bytes` below
`upload_chunk_bytes` is rejected outright by the settings save endpoint
(an incoherent policy), whichever of the two values the request actually
touches.

## Cloudflare Tunnel

`cloudflared` runs on the same host and dials out, so no port is forwarded and
the home IP is never published. The server binds `127.0.0.1:8080`; `cloudflared`
is the only process that can reach it.

```yaml
# /etc/cloudflared/config.yml
tunnel: <tunnel-id>
credentials-file: /etc/cloudflared/<tunnel-id>.json
ingress:
  - hostname: media.example.com
    service: http://127.0.0.1:8080
  - service: http_status:404
```

Two settings follow from terminating TLS at the edge:

- Run the server without `-insecure-cookies`. The origin sees plain HTTP, so
  the session cookie's `Secure` flag comes from this config, not from
  inspecting the request; the server never sees `r.TLS` set and never
  consults it.
- `-base-url` is the public `https://` origin, not the loopback address.

## Client IP: what is trusted and why

The public surface an unauthenticated client can reach is exactly what
`IsPublicPath` in `internal/web/middleware.go` allowlists: `/login`,
`/robots.txt`, `/favicon.ico`, and anything under `/s/` (once that route
lands), `/m/`, `/t/`, or `/static/`. Everything else requires a session
cookie or a Bearer API key.

`clientIP()`, also in `internal/web/middleware.go`, is the single source of
the caller's address used everywhere an IP matters: the login rate limiter
(5 attempts per 15 minutes, keyed per IP) and the public-route rate limiter
(240 requests per minute, keyed per IP, covering `/m/`, `/t/`, and `/s/` once
it lands). It reads `CF-Connecting-IP` first, then falls back to
`RemoteAddr`. It deliberately never trusts `X-Forwarded-For`.

This is only safe because of the topology above: the origin binds loopback
and `cloudflared` is the only process that can reach it, so
`CF-Connecting-IP` is set by our own tunnel daemon and cannot be forged by a
remote client. **If you ever put a different proxy in front of this server
instead of `cloudflared`, `clientIP()` must change with it.** A generic
reverse proxy will not set `CF-Connecting-IP`, and blindly trusting
`X-Forwarded-For` from an arbitrary client would hand a login brute-forcer a
fresh rate-limit bucket on every request, and would let a public-route
scraper spoof its way past the throttle too. Nothing else in the server
depends on the forwarding story; this one function is the entire blast
radius of that change.

## Cloudflare dashboard rules: required, not optional

Everything is proxied through Cloudflare on a tunnel; there is no DNS-only
path to fall back on, so these are configuration, not preferences.

- **WAF / Bot Fight Mode must skip crawler user agents on `/s/*`, `/m/*` and
  `/t/*`.** A challenged Discordbot caches a *failed* embed, and the bad
  embed sticks around after the challenge is lifted.
- **Cache Rule: bypass cache on `/m/*` and `/t/*`.** This keeps the media
  library out of Cloudflare's CDN, which is what self-serve Terms of Service
  section 2.8 actually restricts, and stops stale bytes being served after a
  purge.
- `/s/`, `/m/`, `/t/` are the only anonymous *content* routes (`/login`,
  `/robots.txt`, `/favicon.ico`, and `/static/` are also public but serve no
  media); everything else already requires a session or Bearer key, so
  exposing exactly these three prefixes is expected and safe.

Note: as of this document, `/s/{id}` (the embed page) has not yet been wired
into the router in this checkout; it lands via a separate task in the same
plan. Set these rules up now regardless, since the public-path allowlist and
the rate limiter above already treat `/s/` as public and will apply to it the
moment the route is merged.

## systemd unit

`/etc/systemd/system/boobies-media.service`:

```ini
[Unit]
Description=boobies-media
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=media
Group=media
# Tools must be on PATH; a distro upgrade that renames or drops one shows up
# as a soft-fail entry in the admin dependency banner rather than a crash.
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=/srv/media/bin/server -addr 127.0.0.1:8080 -data /srv/media -base-url https://media.example.com
Restart=on-failure
RestartSec=2
# Hardening: the process only needs its data dir.
ProtectSystem=strict
ReadWritePaths=/srv/media
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now boobies-media
journalctl -u boobies-media -f
```

## Backups

Placeholder: the nightly SQLite backup timer (`VACUUM INTO`, keeping the
newest 7 copies under `data/backups/`) is being implemented as a separate
task in this same plan and had not landed in this checkout at the time this
document was written. Fill in this section once that lands, including
whatever offsite-copy step it ends up needing.

## Dependencies and soft failure

At startup the server probes for `yt-dlp`, `gallery-dl`, `ffmpeg`, `ffprobe`
and `cwebp` on `PATH` (`internal/deps`). `ffmpeg`, `ffprobe` and `cwebp` are
probed with `-version` (their argv parser only accepts a single leading
dash); `yt-dlp` and `gallery-dl` are probed with `--version`. A missing or
failing tool is logged as a **warning**, never a fatal error: the server
starts regardless, and only the features that need that specific tool fail,
with a message naming it. The admin dependency banner lists every tool's
status and reported version so a broken or stale tool is visible without
digging through logs.

## Cookie maintenance (recurring)

Unauthenticated Twitter/X access no longer exists, and YouTube/TikTok tighten
periodically. Export browser cookies to a Netscape-format file and point the
admin Settings page at it.

As of this checkout, only the `cookies_twitter` setting is actually wired up:
it is a real, admin-settable value (`POST /api/admin/settings`) with a
built-in default of empty (not configured). The admin page also lists
`cookies_youtube`, `cookies_tiktok`, `cookies_medal`, and
`min_free_disk_bytes` as fields, but none of the four is registered as a
known setting yet, so each renders blank and saving any of them is rejected
with a `400 unknown_setting` error. Filling those in is scoped to a later,
separate piece of work (the `internal/ingest` package this settings list was
written ahead of). Do not rely on them until that lands.

The admin page's ingest self-test buttons are present in the UI as well
(one per source), but the endpoint they call is not yet registered on the
server in this checkout; treat them the same way, as forthcoming rather than
usable today.

Cookies expire. When a source starts failing, re-export from a logged-in
browser session and re-save the path.

## Upgrading tools

`yt-dlp` staleness is the number-one cause of ingest breakage. The admin page
shows `yt-dlp --version`; update it (`yt-dlp -U`, or your package manager)
when YouTube/TikTok ingests start failing.

## What breaks and how you will know

- **A single upload chunk exceeds `upload_chunk_bytes`.** The server itself
  rejects it with `413 chunk_too_large` before it ever risks Cloudflare's own
  cap. If Cloudflare's `413` shows up instead (visible in the browser as a
  failed PUT with no server-side log line at all), `upload_chunk_bytes` is
  set above what the tunnel will pass, which the settings save endpoint's own
  64 MiB ceiling should prevent; check that nothing bypassed that endpoint
  (a direct database edit, for instance).
- **A dependency is missing or broken.** Nothing crashes. Check the admin
  dependency banner, or `journalctl -u boobies-media` for the startup
  warning line naming the tool; the specific ingest or thumbnail feature
  that needed it will fail on use with a message naming the same tool.
- **The data disk fills up.** There is currently no automated free-disk
  check in the server (the `min_free_disk_bytes` setting is a placeholder
  field, not yet wired to anything, see above); a full disk surfaces as
  write failures from the OS, most visibly a failed upload completion or a
  failed job with an I/O error in its error string in the admin job list.
  Monitor free space on `-data` yourself (a `df` cron, a systemd
  `ConditionPathIsMountPoint` check, or ordinary disk-space alerting) until
  an in-app check exists.
- **A Cloudflare-side extractor cookie goes stale** (Twitter/X, YouTube,
  TikTok, Medal). The specific source starts failing; nothing else does.
  Re-export the cookie file for that source and re-save its path in
  Settings (currently only `cookies_twitter` can actually be saved, see
  above).
- **Something other than `cloudflared` terminates TLS in front of this
  server** (a plain nginx or Caddy reverse proxy, for example). Three things
  change at once and all three must be handled together: `clientIP()` in
  `internal/web/middleware.go` must read a header your new proxy actually
  sets (and only from a proxy you trust, never straight from the client), a
  proxy body-size limit (nginx's default 1 MB `client_max_body_size`, for
  example) must be raised above `upload_chunk_bytes`, and the server should
  usually keep running without `-insecure-cookies` since most reverse
  proxies still terminate real TLS. Skipping the `clientIP()` change quietly
  breaks both rate limiters, since any client could then forge whatever
  header your proxy forwards and get a fresh budget on every request.

## Security note: admin gate and permanent deletion

`/admin` and every route under `/api/admin/*`, plus `/api/jobs/{id}/retry`,
run through `requireAdmin` (`internal/web/middleware_admin.go`). A signed-in
non-admin gets a `403` (JSON for `/api/` paths, a plain 403 page otherwise),
independent of what the navigation UI happens to show or hide.

This matters because `db.PurgeItem` and `media.Store.Purge` take no actor
argument at all; authorizing a permanent delete is entirely the caller's
job. `requireAdmin` on the purge route is the only barrier standing between
a signed-in non-admin and permanently destroying someone else's media.
Anonymous requests never reach that far (`Server.Gate` redirects them to
`/login` first), but that gate is defense in depth, not the actual control.
