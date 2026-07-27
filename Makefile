GO  ?= go
BUN ?= bun

.PHONY: all assets build test race dev clean

all: build

# Bundle the TypeScript islands and CSS into web/static/dist.
assets:
	$(BUN) run build

build: assets
	$(GO) build -o bin/server ./cmd/server

test:
	@fmt_out=$$(gofmt -l cmd internal web); if [ -n "$$fmt_out" ]; then echo "gofmt needed on:"; echo "$$fmt_out"; exit 1; fi
	$(GO) test ./... -count=1

race:
	$(GO) test ./... -race -count=1

# Local development: plain HTTP on loopback, so the Secure cookie flag is off.
dev: assets
	$(GO) run ./cmd/server -addr 127.0.0.1:8080 -data ./data -base-url http://localhost:8080 -insecure-cookies

clean:
	rm -rf bin web/static/dist
