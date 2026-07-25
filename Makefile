.PHONY: build test lint compose-up compose-down demo-static docs

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/dennisme/grex/internal/buildinfo.Version=$(VERSION) \
           -X github.com/dennisme/grex/internal/buildinfo.Commit=$(COMMIT) \
           -X github.com/dennisme/grex/internal/buildinfo.Date=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" ./...

test:
	go test -race ./...

lint:
	golangci-lint run

compose-up:
	docker compose up -d --build --wait

compose-down:
	docker compose down -v

# Sync live UI assets into the static GitHub Pages demo.
demo-static:
	mkdir -p docs/demo/static
	cp internal/ui/static/app.css \
		internal/ui/static/theme.js \
		internal/ui/static/matcher.js \
		internal/ui/static/favicon.png \
		internal/ui/static/logo-mark.png \
		"internal/ui/static/logo-mark@2x.png" \
		docs/demo/static/
	# Demo hooks: in-memory attribute autocomplete + re-init after SPA re-renders.
	# Keep these patches in sync with docs/demo/static/matcher.js expectations.
	node scripts/patch-demo-matcher.js

docs: demo-static
	mkdir -p docs/assets && cp logo.png docs/assets/logo.png
	mkdocs build --strict
