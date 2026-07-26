# CLI reference

## Synopsis

```text
grex -config <path>
```

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `config.yaml` | Path to the grex YAML configuration file |

There are no subcommands. All runtime behavior is controlled by the config
file and `GREX_*` environment variables ([Configuration](../admin/configuration.md)).

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Clean shutdown after signal |
| non-zero | Config error, listener failure, or shutdown failure; message on stderr prefixed with `grex:` |

## Signals

| Signal | Behavior |
|--------|----------|
| `SIGINT`, `SIGTERM` | Drain (`/readyz` 503), wait 5s, shutdown listeners (10s grace) |

## Build-time version

Version strings are not CLI flags. They are injected at compile time:

```text
-X github.com/dennisme/grex/internal/buildinfo.Version=…
-X github.com/dennisme/grex/internal/buildinfo.Commit=…
-X github.com/dennisme/grex/internal/buildinfo.Date=…
```

See `justfile` and `Dockerfile`. Unset builds report `dev` / `none` /
similar placeholders via package defaults.
