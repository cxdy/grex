.PHONY: init build test coverage lint markdownlint pre-commit compose-up compose-down demo-static docs

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/dennisme/grex/internal/buildinfo.Version=$(VERSION) \
           -X github.com/dennisme/grex/internal/buildinfo.Commit=$(COMMIT) \
           -X github.com/dennisme/grex/internal/buildinfo.Date=$(DATE)

# Install tool versions (mise) and git hooks.
init:
	@command -v mise >/dev/null 2>&1 && mise install || true
	@command -v pre-commit >/dev/null 2>&1 && pre-commit install || \
		(echo "pre-commit not found; install via 'pip install pre-commit' or mise" && false)

build:
	go build -ldflags "$(LDFLAGS)" ./...

test:
	env -u GOROOT GOTOOLCHAIN=auto go test -race ./...

# Profile without -race for cobertura (race + cover is slower / noisier in CI).
coverage:
	env -u GOROOT GOTOOLCHAIN=auto go test -count=1 ./... \
		-coverprofile coverage.out -covermode count
	env -u GOROOT GOTOOLCHAIN=auto go tool cover -html=coverage.out -o coverage.html
	env -u GOROOT GOTOOLCHAIN=auto go run github.com/boumenot/gocover-cobertura@v1.4.0 \
		--by-files -ignore-gen-files < coverage.out > coverage.xml

lint:
	env -u GOROOT GOTOOLCHAIN=auto golangci-lint run

markdownlint:
	npx --yes markdownlint-cli2

pre-commit:
	pre-commit run --all-files

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
