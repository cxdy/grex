# Configuration

Configuration is a single **YAML** file. After the file is loaded, matching
**`GREX_*` environment variables** override individual fields. Unknown YAML
keys are rejected (`KnownFields(true)`).

Copy `config.example.yaml` for a fully commented template.

## Load order

1. Built-in defaults
2. YAML file (`-config` path)
3. Environment variable overrides
4. Validation (fail fast on bad addresses, TLS paths, enums)

## Reference

### `listeners`

| Field | Default | Env | Description |
|-------|---------|-----|-------------|
| `opamp` | `:4320` | `GREX_LISTENERS_OPAMP` | OpAMP bind address (`host:port`) |
| `ui` | `:8080` | `GREX_LISTENERS_UI` | Web UI + JSON API |
| `telemetry` | `:9090` | `GREX_LISTENERS_TELEMETRY` | Health + Prometheus (+ optional pprof) |

Each value must be a valid `host:port` for `net.SplitHostPort`.

### `opamp_tls`, `ui_tls`, `telemetry_tls`

One block per listener, same shape:

| Field | Default | Env (`opamp_tls` shown) | Description |
|-------|---------|-----|-------------|
| `cert_file` | `""` | `GREX_OPAMP_TLS_CERT_FILE` | Server certificate PEM; empty = plaintext |
| `key_file` | `""` | `GREX_OPAMP_TLS_KEY_FILE` | Server private key PEM; must pair with `cert_file` |
| `client_ca_file` | `""` | `GREX_OPAMP_TLS_CLIENT_CA_FILE` | Client CA for mTLS; requires cert+key |

`ui_tls` and `telemetry_tls` use the equivalent `GREX_UI_TLS_*` and
`GREX_TELEMETRY_TLS_*` env vars. Paths must exist at startup when set. See
[TLS and mTLS](tls-mtls.md).

### `auth`

Applies only to a listener whose TLS block also sets `client_ca_file`.

| Field | Default | Env | Description |
|-------|---------|-----|-------------|
| `role_mapping` | `[]` | — | Ordered list of `{match: exact\|prefix, spiffe_id, role: viewer\|admin}` |
| `default_role` | `none` | `GREX_AUTH_DEFAULT_ROLE` | Role for an authenticated caller matching no rule; `none` denies |

`role_mapping` is evaluated exact-before-prefix regardless of list order;
among rules of the same specificity, the first match wins. See
[Authentication](authentication.md).

### `metrics`

| Field | Default | Env | Description |
|-------|---------|-----|-------------|
| `per_agent_series_limit` | `1000` | `GREX_METRICS_PER_AGENT_SERIES_LIMIT` | Cap for per-`instance_uid` series; non-negative |

See [Cardinality](../observability/cardinality.md).

### `ui`

| Field | Default | Env | Description |
|-------|---------|-----|-------------|
| `poll_interval` | `5s` | `GREX_UI_POLL_INTERVAL` | htmx refresh interval for fleet/status pages; must be &gt; 0 |

### `fleet`

| Field | Default | Env | Description |
|-------|---------|-----|-------------|
| `heartbeat_interval` | `30s` | `GREX_FLEET_HEARTBEAT_INTERVAL` | Expected check-in period; must be &gt; 0 |
| `stale_missed_heartbeats` | `3` | `GREX_FLEET_STALE_MISSED_HEARTBEATS` | Missed intervals before eviction; must be &gt; 0 |
| `required_attributes` | `[]` | `GREX_FLEET_REQUIRED_ATTRIBUTES` | Attribute keys every agent should report; comma-separated in env |

Missing required attributes are logged and counted; agents are still
accepted (observe-only). See [Fleet state](../developer/fleet-state.md).

### `debug`

| Field | Default | Env | Description |
|-------|---------|-----|-------------|
| `pprof_enabled` | `false` | `GREX_DEBUG_PPROF_ENABLED` | Mount `/debug/pprof/*` on the telemetry listener |

See [Debug pprof](../observability/debug-pprof.md).

### `log`

| Field | Default | Env | Description |
|-------|---------|-----|-------------|
| `level` | `info` | `GREX_LOG_LEVEL` | `debug`, `info`, `warn`, `error` |
| `format` | `text` | `GREX_LOG_FORMAT` | `text` or `json` (stderr) |

See [Logging](../observability/logging.md).

## Example

```yaml
listeners:
  opamp: ":4320"
  ui: ":8080"
  telemetry: ":9090"

opamp_tls:
  cert_file: /certs/server.pem
  key_file: /certs/server-key.pem
  client_ca_file: /certs/ca.pem

ui_tls:
  cert_file: /certs/server.pem
  key_file: /certs/server-key.pem
  client_ca_file: /certs/ca.pem

telemetry_tls:
  cert_file: /certs/server.pem
  key_file: /certs/server-key.pem
  client_ca_file: /certs/ca.pem

auth:
  default_role: none
  role_mapping:
    - match: exact
      spiffe_id: spiffe://grex-api.internal/user/alice
      role: viewer
    - match: exact
      spiffe_id: spiffe://grex-api.internal/service/prometheus
      role: viewer

metrics:
  per_agent_series_limit: 1000

ui:
  poll_interval: 5s

fleet:
  heartbeat_interval: 30s
  stale_missed_heartbeats: 3
  required_attributes:
    - deployment.environment
    - service.namespace

debug:
  pprof_enabled: false

log:
  level: info
  format: json
```

Environment override example:

```sh
export GREX_LOG_LEVEL=debug
export GREX_FLEET_REQUIRED_ATTRIBUTES=deployment.environment,service.namespace
grex -config /etc/grex/config.yaml
```

## Not configurable yet

OIDC client settings (Authorization Code flow against Dex, `groups`-based
role mapping) are not yet present in `internal/config`. They appear in the
[design SPEC](../spec/design.md) milestone 7 and are tracked in
[issue #11](https://github.com/dennisme/grex/issues/11). mTLS auth for the
UI and telemetry listeners, described above, has shipped.
