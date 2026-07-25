# Logging

## How grex logs

grex uses Go’s standard **`log/slog`** package. Logs go to **stderr** only.
There is no file sink, syslog driver, or OTLP log exporter inside grex.

### Configuration

| Field | Values | Env |
|-------|--------|-----|
| `log.level` | `debug`, `info`, `warn`, `error` | `GREX_LOG_LEVEL` |
| `log.format` | `text`, `json` | `GREX_LOG_FORMAT` |

Defaults: `info` + `text`.

Production tip: set `format: json` for log aggregators. Compose uses
`debug` + `json` so smoke scripts and developers can see per-agent
lifecycle lines.

## What you will see

Examples of intentional log themes (not an exhaustive schema):

- Listener start / shutdown / drain
- Config-related fatal errors (process exits)
- OpAMP message handling issues
- Gateway `connect` metadata (headers/remote address) at appropriate levels
- Missing required attributes (warnings)
- pprof enabled warning at startup

Exact attribute keys on log records may evolve; treat message strings and
levels as the stable contract unless tests pin a field.

## Collecting logs

Because grex only writes stderr:

| Runtime | Approach |
|---------|----------|
| Docker / Compose | Container logging driver → your stack |
| systemd | `journald` captures stderr |
| Kubernetes | Node log agent / platform logging (stdout/stderr) |

grex does not ship log pipelines. Forward whatever your platform already
uses for other Go services.

## Levels in practice

| Level | Use |
|-------|-----|
| `error` | Listener failures, shutdown failures |
| `warn` | pprof enabled; missing attributes; unusual but handled conditions |
| `info` | Lifecycle (default production) |
| `debug` | Verbose per-connection / per-agent detail for labs |

Avoid leaving `debug` on large fleets unless your log system can handle the
volume.

## Correlation with metrics

There is no automatic trace_id on log lines today. Correlate operationally
via:

- Time windows around metric spikes
- `instance_uid` when present in a log field
- `grex_build_info` / `grex_config_info` for “which binary/config”
