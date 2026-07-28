FROM oven/bun:1 AS web-builder
WORKDIR /src
COPY package.json bunfig.toml tsconfig.json ./
COPY web ./web
RUN bun run build

FROM golang:1.25-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
COPY --from=web-builder /src/web/static/dist ./web/static/dist
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl ffmpeg python3 python3-venv webp \
    && python3 -m venv /opt/media-tools \
    && /opt/media-tools/bin/pip install --no-cache-dir yt-dlp gallery-dl \
    && rm -rf /var/lib/apt/lists/*

ENV PATH="/opt/media-tools/bin:${PATH}" \
    BM_ADDR="0.0.0.0:8080" \
    BM_DATA_DIR="/data"

WORKDIR /app
COPY --from=go-builder /out/server /app/server
COPY scripts/bm-user /usr/local/bin/bm-user
RUN chmod 0755 /usr/local/bin/bm-user
RUN mkdir -p /data

VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl --fail --silent http://127.0.0.1:8080/robots.txt >/dev/null || exit 1

ENTRYPOINT ["/app/server"]
