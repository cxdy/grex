# grex development commands. Run `just` to list recipes.

set shell := ["bash", "-euo", "pipefail", "-c"]

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
commit  := `git rev-parse --short HEAD 2>/dev/null || echo none`
date    := `date -u +%Y-%m-%dT%H:%M:%SZ`
ldflags := "-X github.com/dennisme/grex/internal/buildinfo.Version=" + version + " -X github.com/dennisme/grex/internal/buildinfo.Commit=" + commit + " -X github.com/dennisme/grex/internal/buildinfo.Date=" + date

# List available recipes.
default:
    @just --list

# Install tool versions (mise) and git hooks.
init:
    command -v mise >/dev/null 2>&1 && mise install || true
    if command -v pre-commit >/dev/null 2>&1; then \
        pre-commit install; \
    else \
        echo "pre-commit not found; install via 'pip install pre-commit' or mise"; \
        false; \
    fi

# Build all packages with version ldflags.
build:
    go build -ldflags "{{ldflags}}" ./...

# Run tests with the race detector.
test:
    env -u GOROOT GOTOOLCHAIN=auto go test -race ./...

# Profile without -race for cobertura (race + cover is slower / noisier in CI).
coverage:
    env -u GOROOT GOTOOLCHAIN=auto go test -count=1 ./... \
        -coverprofile coverage.out -covermode count
    env -u GOROOT GOTOOLCHAIN=auto go tool cover -html=coverage.out -o coverage.html
    env -u GOROOT GOTOOLCHAIN=auto go run github.com/boumenot/gocover-cobertura@v1.4.0 \
        --by-files -ignore-gen-files < coverage.out > coverage.xml

# Run golangci-lint.
lint:
    env -u GOROOT GOTOOLCHAIN=auto golangci-lint run

# Lint markdown with markdownlint-cli2.
markdownlint:
    npx --yes markdownlint-cli2

# Run pre-commit on all files.
pre-commit:
    pre-commit run --all-files

# Build and start the full compose stack.
compose-up:
    docker compose up -d --build --wait

# Tear down the compose stack, removing volumes.
compose-down:
    docker compose down -v

# Migrates grex's own tables (not River's — see internal/persistence/migrations
# and cmd/river-migrate). Placeholder migration only until jobs/permission
# schema is decided (docs/spec/design.md Open questions).
migrate_version := "v4.19.1"
database_url := env("DATABASE_URL", "postgres://grex:grex-dev-password@localhost:5432/grex?sslmode=disable")

migrate-up:
    env -u GOROOT GOTOOLCHAIN=auto go run -tags 'postgres' \
        github.com/golang-migrate/migrate/v4/cmd/migrate@{{migrate_version}} \
        -path internal/persistence/migrations -database "{{database_url}}" up

migrate-down:
    env -u GOROOT GOTOOLCHAIN=auto go run -tags 'postgres' \
        github.com/golang-migrate/migrate/v4/cmd/migrate@{{migrate_version}} \
        -path internal/persistence/migrations -database "{{database_url}}" down

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

# Sync demo UI assets + mkdocs build --strict (packages Helm chart when helm is installed).
docs: demo-static
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p docs/assets && cp logo.png docs/assets/logo.png
    # Pull GitHub Release notes into docs/releases/changelog.md
    python3 scripts/generate-releases-changelog.py
    mkdocs build --strict
    # Package the Helm chart into site/charts so local mkdocs previews match
    # GitHub Pages (docs + demo + chart repo share one site).
    if command -v helm >/dev/null 2>&1; then
        mkdir -p site/charts
        helm package deploy/charts/grex --destination site/charts
        helm repo index site/charts --url https://dennisme.github.io/grex/charts
        cp deploy/charts/grex/README.md site/charts/README.md
    else
        echo "helm not found; skipping chart packaging into site/charts"
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

# End-to-end chart install into kind or k3d (auto-detect; see deploy/charts/smoke.sh --help).
helm-e2e:
    ./deploy/charts/smoke.sh

# End-to-end chart install with kind.
helm-e2e-kind:
    ./deploy/charts/smoke.sh --provider kind

# End-to-end chart install with k3d (k3s).
helm-e2e-k3d:
    ./deploy/charts/smoke.sh --provider k3d

# Create and push the next semver tag from conventional commits (svu).
# Also bumps deploy/charts/grex Chart.yaml version + appVersion to match so the
# Pages chart repo and default image tag stay aligned with the release.
# Preview only: svu next
release-tag:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! command -v svu >/dev/null 2>&1; then
        echo "svu is required: https://github.com/caarlos0/svu"
        echo "  brew install svu"
        echo "  # or: go install github.com/caarlos0/svu/v3@latest"
        exit 1
    fi
    if [[ -n "$(git status --porcelain)" ]]; then
        echo "Working tree is dirty; commit or stash before releasing"
        exit 1
    fi
    git fetch --tags --quiet
    TAG=$(svu next)
    if git rev-parse "$TAG" >/dev/null 2>&1; then
        echo "Tag $TAG already exists (nothing new to release, or fetch remote tags)"
        exit 1
    fi
    # Helm chart version / appVersion are SemVer without a leading "v".
    VER="${TAG#v}"
    CHART_YAML=deploy/charts/grex/Chart.yaml
    tmp=$(mktemp)
    sed -E \
        -e "s/^version: .*/version: ${VER}/" \
        -e "s/^appVersion: .*/appVersion: \"${VER}\"/" \
        "$CHART_YAML" >"$tmp"
    mv "$tmp" "$CHART_YAML"
    if [[ -n "$(git status --porcelain -- "$CHART_YAML")" ]]; then
        echo "Bumping Helm chart version/appVersion to ${VER}"
        git add "$CHART_YAML"
        git commit -m "chore(release): bump helm chart to ${VER}"
    else
        echo "Helm chart already at ${VER}"
    fi
    echo "Creating tag $TAG"
    git tag "$TAG"
    echo "Pushing commit(s) and tag $TAG to origin"
    git push origin HEAD
    git push origin "$TAG"
