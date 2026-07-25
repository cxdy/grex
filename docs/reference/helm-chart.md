# Helm chart reference

Technical reference for the **grex** Helm chart (`deploy/charts/grex`).
For install steps and operational guidance, see
[Deploy with Helm](../admin/helm.md).

## Chart metadata

| Field | Value |
|-------|--------|
| Name | `grex` |
| Type | `application` |
| Chart version | `0.1.0` (see `Chart.yaml`) |
| App version | tracks grex release / image tag |
| Kubernetes | `>= 1.25.0-0` |
| Source | [github.com/dennisme/grex](https://github.com/dennisme/grex) |

## Repository

```text
https://dennisme.github.io/grex/charts/
```

Published by the docs GitHub Actions workflow into the **same** GitHub Pages
site as MkDocs and the static demo, under the `/charts/` prefix only
(`index.yaml` + packaged `.tgz`). Docs and demo paths are unchanged.

```sh
helm repo add grex https://dennisme.github.io/grex/charts/
helm install grex grex/grex -n grex --create-namespace
```

Local path install:

```sh
helm install grex ./deploy/charts/grex -n grex --create-namespace
```

## Resources created

| Template | Kind | Condition |
|----------|------|-----------|
| `deployment.yaml` | Deployment | always |
| `service.yaml` | Service | always |
| `configmap.yaml` | ConfigMap | always |
| `serviceaccount.yaml` | ServiceAccount | `serviceAccount.create` |
| `ingress.yaml` | Ingress | `ingress.enabled` |
| `hpa.yaml` | HorizontalPodAutoscaler | `autoscaling.enabled` |
| `servicemonitor.yaml` | ServiceMonitor ×2 | `serviceMonitor.enabled` |
| `opamp-gateway.yaml` | ConfigMap + Service + Deployment | `opampGateway.enabled` |
| `tests/test-connection.yaml` | Pod (helm test hook) | always (test hook) |

## Service ports

| Name | Default port | grex listener |
|------|--------------|---------------|
| `opamp` | `4320` | OpAMP (WS / HTTP) |
| `ui` | `8080` | Web UI + JSON API |
| `telemetry` | `9090` | Health + Prometheus |

Container ports match Service ports. Bind addresses inside the pod come from
`listeners.*` (defaults `:4320`, `:8080`, `:9090`).

## Values

Defaults live in `deploy/charts/grex/values.yaml`. Override with
`-f my-values.yaml` or `--set`.

### Global / naming

| Key | Default | Description |
|-----|---------|-------------|
| `nameOverride` | `""` | Override chart name in labels |
| `fullnameOverride` | `""` | Override full resource name |

### Image

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/dennisme/grex` | Image repository |
| `image.tag` | `""` | Tag; empty → `Chart.AppVersion` |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `imagePullSecrets` | `[]` | Secrets for private registries |

### Deployment

| Key | Default | Description |
|-----|---------|-------------|
| `replicaCount` | `1` | Pods; keep at 1 (in-memory fleet state) |
| `podAnnotations` | `{}` | Extra pod annotations |
| `podLabels` | `{}` | Extra pod labels |
| `podSecurityContext` | non-root uid/gid 1000 | Pod securityContext |
| `securityContext` | drop ALL, read-only root | Container securityContext |
| `resources` | requests/limits set | CPU/memory |
| `nodeSelector` | `{}` | Node selector |
| `tolerations` | `[]` | Tolerations |
| `affinity` | `{}` | Affinity |
| `priorityClassName` | `""` | PriorityClass |
| `terminationGracePeriodSeconds` | `30` | Must exceed drain + shutdown grace |
| `extraEnv` | `[]` | Extra env vars (`GREX_*` ok) |
| `extraVolumeMounts` | `[]` | Extra mounts |
| `extraVolumes` | `[]` | Extra volumes |

### Service account

| Key | Default | Description |
|-----|---------|-------------|
| `serviceAccount.create` | `true` | Create a ServiceAccount |
| `serviceAccount.automount` | `true` | Automount API token |
| `serviceAccount.annotations` | `{}` | SA annotations |
| `serviceAccount.name` | `""` | Name; empty → fullname |

### Service

| Key | Default | Description |
|-----|---------|-------------|
| `service.type` | `ClusterIP` | Service type |
| `service.annotations` | `{}` | Service annotations |
| `service.ports.opamp.port` | `4320` | OpAMP port |
| `service.ports.ui.port` | `8080` | UI/API port |
| `service.ports.telemetry.port` | `9090` | Telemetry port |
| `service.ports.*.nodePort` | `null` | Optional NodePort |
| `service.ports.*.protocol` | `TCP` | Protocol |

### Listeners (grex config)

| Key | Default | Description |
|-----|---------|-------------|
| `listeners.opamp` | `":4320"` | OpAMP bind address |
| `listeners.ui` | `":8080"` | UI bind address |
| `listeners.telemetry` | `":9090"` | Telemetry bind address |

Ports in these addresses should match `service.ports.*`.

### Config (grex YAML)

Rendered into ConfigMap key `config.yaml`.

| Key | Default | Description |
|-----|---------|-------------|
| `config.metrics.per_agent_series_limit` | `1000` | Per-agent series cap |
| `config.ui.poll_interval` | `5s` | UI htmx poll interval |
| `config.fleet.heartbeat_interval` | `30s` | Expected check-in period |
| `config.fleet.stale_missed_heartbeats` | `3` | Misses before eviction |
| `config.fleet.required_attributes` | `[]` | Observe-only required keys |
| `config.debug.pprof_enabled` | `false` | Mount pprof on telemetry |
| `config.log.level` | `info` | Log level |
| `config.log.format` | `json` | `text` or `json` |

TLS file paths are injected automatically when `tls.enabled` is true (see
below). Other grex fields can be forced via `extraEnv` (`GREX_*`).

### TLS (OpAMP)

| Key | Default | Description |
|-----|---------|-------------|
| `tls.enabled` | `false` | Mount secret and set grex `tls.*` |
| `tls.existingSecret` | `""` | **Required** when enabled |
| `tls.certKey` | `tls.crt` | Cert key in Secret |
| `tls.keyKey` | `tls.key` | Key key in Secret |
| `tls.clientCAKey` | `ca.crt` | Client CA key; empty string omits mTLS CA |
| `tls.mountPath` | `/etc/grex/certs` | Mount path |
| `tls.certFile` / `keyFile` / `clientCAFile` | `""` | Override paths in grex YAML |

### Probes

| Key | Default | Description |
|-----|---------|-------------|
| `livenessProbe` | HTTP GET `/healthz` on `telemetry` | Liveness |
| `readinessProbe` | HTTP GET `/readyz` on `telemetry` | Readiness |

### Autoscaling

| Key | Default | Description |
|-----|---------|-------------|
| `autoscaling.enabled` | `false` | Create HPA |
| `autoscaling.minReplicas` | `1` | Min replicas |
| `autoscaling.maxReplicas` | `1` | Max (keep 1 until HA) |
| `autoscaling.targetCPUUtilizationPercentage` | `80` | CPU target |
| `autoscaling.targetMemoryUtilizationPercentage` | unset | Memory target |

### Ingress (UI only)

| Key | Default | Description |
|-----|---------|-------------|
| `ingress.enabled` | `false` | Create Ingress |
| `ingress.className` | `""` | IngressClass |
| `ingress.annotations` | `{}` | Annotations |
| `ingress.hosts` | `grex.example.com` → `/` | Hosts/paths |
| `ingress.tls` | `[]` | TLS secrets |

Backend Service port is always named **`ui`**.

### ServiceMonitor

Requires Prometheus Operator CRDs.

| Key | Default | Description |
|-----|---------|-------------|
| `serviceMonitor.enabled` | `false` | Create two ServiceMonitors |
| `serviceMonitor.namespace` | `""` | Empty → release namespace |
| `serviceMonitor.labels` | `{}` | Extra labels (e.g. Prometheus selector) |
| `serviceMonitor.annotations` | `{}` | Annotations |
| `serviceMonitor.server.interval` | `15s` | `/metrics` interval |
| `serviceMonitor.server.path` | `/metrics` | Server path |
| `serviceMonitor.fleet.interval` | `30s` | `/metrics/fleet` interval |
| `serviceMonitor.fleet.path` | `/metrics/fleet` | Fleet path |
| `serviceMonitor.honorLabels` | `true` | honorLabels |
| `serviceMonitor.scheme` | `http` | http/https |
| `serviceMonitor.tlsConfig` | `{}` | Endpoint TLS |
| `serviceMonitor.*.relabelings` | `[]` | Relabel configs |
| `serviceMonitor.*.metricRelabelings` | `[]` | Metric relabel configs |

### Annotation scrape (optional)

| Key | Default | Description |
|-----|---------|-------------|
| `metrics.serviceAnnotations.enabled` | `false` | Add `prometheus.io/*` for `/metrics` only |

Does **not** create a second job for `/metrics/fleet`. Prefer
`serviceMonitor` or external scrape config for dual jobs.

### OpAMP gateway (optional)

| Key | Default | Description |
|-----|---------|-------------|
| `opampGateway.enabled` | `false` | Deploy gateway |
| `opampGateway.replicaCount` | `1` | Gateway replicas |
| `opampGateway.image.*` | `ghcr.io/dennisme/grex-opamp-gateway` | Gateway image |
| `opampGateway.config` | `""` | Full collector YAML; empty → minimal default |
| `opampGateway.service.type` | `ClusterIP` | Gateway Service type |
| `opampGateway.service.port` | `4320` | Listen port for agents |
| `opampGateway.resources` | set | Resources |
| `opampGateway.tls.*` | disabled | Client TLS to grex |

When `opampGateway.config` is empty, the chart renders a minimal
`opampgateway` extension config that dials the grex Service OpAMP endpoint
(`ws` or `wss` depending on `tls.enabled`).

## ConfigMap layout

```yaml
# mounted at /etc/grex/config.yaml
listeners:
  opamp: ":4320"
  ui: ":8080"
  telemetry: ":9090"
# tls: ...          # only if tls.enabled
metrics: { ... }
ui: { ... }
fleet: { ... }
debug: { ... }
log: { ... }
```

The Deployment args are `-config /etc/grex/config.yaml`. A
`checksum/config` annotation on the pod template forces a rollout when the
ConfigMap content changes.

## Helm tests

```sh
helm test grex -n grex
```

The test Pod `wget`s `http://<service>:<telemetry>/healthz`.

## Related

- [Deploy with Helm](../admin/helm.md)
- [Configuration](../admin/configuration.md)
- [Endpoints](endpoints.md)
- [CLI](cli.md)
