.PHONY: init build test coverage lint markdownlint pre-commit compose-up compose-down demo-static docs helm-lint helm-package helm-e2e helm-e2e-kind helm-e2e-k3d release-tag

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
	# Package the Helm chart into site/charts so local mkdocs previews match
	# GitHub Pages (docs + demo + chart repo share one site).
	@if command -v helm >/dev/null 2>&1; then \
		mkdir -p site/charts && \
		helm package deploy/charts/grex --destination site/charts && \
		helm repo index site/charts --url https://dennisme.github.io/grex/charts && \
		cp deploy/charts/grex/README.md site/charts/README.md; \
	else \
		echo "helm not found; skipping chart packaging into site/charts"; \
	fi

# Lint and dry-render the grex Helm chart (requires helm).
helm-lint:
	helm lint deploy/charts/grex
	helm template grex deploy/charts/grex >/dev/null

# Package the chart to dist/charts/ (local only; CI publishes via docs workflow).
helm-package:
	mkdir -p dist/charts
	helm package deploy/charts/grex --destination dist/charts
	helm repo index dist/charts --url https://dennisme.github.io/grex/charts

# End-to-end chart install into kind or k3d (auto-detect). Requires docker,
# kubectl, helm, and kind or k3d. See deploy/charts/smoke.sh --help.
helm-e2e:
	./deploy/charts/smoke.sh

helm-e2e-kind:
	./deploy/charts/smoke.sh --provider kind

helm-e2e-k3d:
	./deploy/charts/smoke.sh --provider k3d

# Create and push the next semver tag from conventional commits (svu).
# Tag push triggers GoReleaser (.github/workflows/goreleaser.yaml).
# Preview only: svu next
release-tag:
	@command -v svu >/dev/null 2>&1 || { \
		echo "svu is required: https://github.com/caarlos0/svu"; \
		echo "  brew install svu"; \
		echo "  # or: go install github.com/caarlos0/svu/v3@latest"; \
		exit 1; \
	}
	@git fetch --tags --quiet
	@TAG=$$(svu next); \
	if git rev-parse "$$TAG" >/dev/null 2>&1; then \
		echo "Tag $$TAG already exists (nothing new to release, or fetch remote tags)"; \
		exit 1; \
	fi; \
	echo "Creating tag $$TAG"; \
	git tag "$$TAG"; \
	echo "Pushing $$TAG to origin"; \
	git push origin "$$TAG"
