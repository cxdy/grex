# Web UI

Package `internal/ui` serves a **server-rendered**, read-only ops UI. There
is no Node toolchain and no CDN at runtime.

## Stack

| Piece | Choice |
|-------|--------|
| Templates | `html/template` |
| Interactivity | Vendored [htmx](https://htmx.org/) |
| CSS | Hand-written design tokens (`static/app.css`) |
| Assets | `go:embed` (`templates/`, `static/`) |
| Escape hatch | [templ](https://github.com/a-h/templ) if template complexity outgrows stdlib |

## Routes

| Method / path | Purpose |
|---------------|---------|
| `GET /` | Fleet page |
| `GET /partials/agents` | Fleet table partial (htmx poll) |
| `GET /agents/{id}` | Agent detail |
| `GET /partials/agents/{id}` | Agent partial (manual refresh) |
| `GET /status` | Status page |
| `GET /partials/status` | Status partial (htmx poll) |
| `GET /static/…` | CSS, JS, logos, favicon |

Mount **after** API routes so `/api/*` is not captured by UI patterns.

## Data access

The UI reads `fleet.Registry` directly (same process). Filter parsing goes
through `api.ParseFilters` so URL semantics match the JSON API. List
projections use the same view helpers as the API.

## Live updates

- Fleet and status pages poll partials every `ui.poll_interval` (default 5s)
- Poll URLs preserve filter/sort query strings
- Agent detail does **not** auto-poll (scroll-friendly config viewing)
- `prefers-reduced-motion` is respected in CSS transitions

## Visual system

Dark ops aesthetic aligned with the grex logo (charcoal/teal/cream/mint).
See comments in `static/app.css` and the design SPEC for token values.
Status uses SVG icons, not emoji; focus rings and contrast targets are
intentional for dense dashboards.

## Adding UI surface area

1. Add or extend a template under `templates/`
2. Register a route in `Handler.Mount`
3. Prefer partials for htmx targets
4. Keep presentation helpers in the `FuncMap` (or small Go helpers)
5. Add tests in `ui_test.go` for HTTP status and key markup when behavior is
   non-trivial

Do not push fleet business rules into templates—compute on the Go side
(mirroring `fleet.RoleOf`, status helpers, etc.).
