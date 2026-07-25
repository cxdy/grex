# Deploy with Helm

This guide deploys grex on Kubernetes with the official Helm chart. Use it
when you already have a cluster and want a production-shaped install (named
ports, ConfigMap config, optional TLS, Ingress, and Prometheus Operator
scrapes). For a laptop lab with collectors included, prefer the
[Compose stack](../getting-started/compose-stack.md) instead.

## Prerequisites

- Kubernetes **1.25+**
- [Helm](https://helm.sh/) **3.14+**
- A grex container image your cluster can pull

Release images on GHCR land with the GoReleaser milestone. Until then, build
and push (or load into a kind/minikube node) yourself:

```sh
docker build -t ghcr.io/dennisme/grex:0.1.0 .
# push or kind load docker-image ghcr.io/dennisme/grex:0.1.0
```

## Add the chart repository

The chart is published on the same GitHub Pages site as the documentation
and static demo, under a dedicated **`/charts/`** path so the three do not
collide:

| Path | Content |
|------|---------|
| `/` | MkDocs documentation |
| `/demo/` | Static fleet UI demo |
| `/charts/` | Helm repository (`index.yaml` + `.tgz`) |

```sh
helm repo add grex https://dennisme.github.io/grex/charts/
helm repo update
helm search repo grex
```

From a git checkout you can install the chart directory directly:

```sh
helm install grex ./deploy/charts/grex --namespace grex --create-namespace
```

## Minimal install

```sh
helm install grex grex/grex \
  --namespace grex \
  --create-namespace \
  --set image.repository=ghcr.io/dennisme/grex \
  --set image.tag=0.1.0
```

Verify:

```sh
kubectl -n grex get pods,svc
kubectl -n grex port-forward svc/grex 8080:8080 9090:9090
curl -sS http://127.0.0.1:9090/healthz   # ok
open http://127.0.0.1:8080/              # fleet UI
```

Collectors should dial the OpAMP Service (default port **4320**):

```text
ws://grex.grex.svc.cluster.local:4320/v1/opamp
```

## Production-shaped install

Create a values file (for example `grex-values.yaml`):

```yaml
image:
  repository: ghcr.io/dennisme/grex
  tag: "0.1.0"

replicaCount: 1   # fleet state is in-memory; do not scale out yet

config:
  log:
    level: info
    format: json
  fleet:
    heartbeat_interval: 30s
    stale_missed_heartbeats: 3
    required_attributes:
      - deployment.environment
      - service.namespace

tls:
  enabled: true
  existingSecret: grex-opamp-tls
  # Secret keys default to tls.crt, tls.key, ca.crt

ingress:
  enabled: true
  className: nginx
  hosts:
    - host: grex.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: grex-ui-tls
      hosts:
        - grex.example.com

serviceMonitor:
  enabled: true
  # labels:
  #   release: kube-prometheus-stack
```

Create the OpAMP TLS Secret first (server cert, key, and client CA for mTLS):

```sh
kubectl -n grex create secret generic grex-opamp-tls \
  --from-file=tls.crt=./server.pem \
  --from-file=tls.key=./server-key.pem \
  --from-file=ca.crt=./ca.pem
```

Install or upgrade:

```sh
helm upgrade --install grex grex/grex \
  --namespace grex \
  --create-namespace \
  -f grex-values.yaml
```

With TLS enabled, collectors use:

```text
wss://grex.grex.svc.cluster.local:4320/v1/opamp
```

and must present a client certificate signed by the CA in `ca.crt`.

## What gets deployed

| Resource | Role |
|----------|------|
| **Deployment** `grex` | Single grex process, three container ports |
| **Service** `grex` | Named ports `opamp`, `ui`, `telemetry` |
| **ConfigMap** | grex YAML at `/etc/grex/config.yaml` |
| **ServiceAccount** | Dedicated SA (token automount optional) |
| **Ingress** (optional) | Routes to the **UI** port only |
| **ServiceMonitor** ×2 (optional) | `/metrics` and `/metrics/fleet` |
| **OpAMP gateway** (optional) | Multiplexing collector; off by default |

Probes use the telemetry listener: liveness → `/healthz`, readiness →
`/readyz`. Termination grace defaults to **30s**, which covers grex’s drain
delay (5s) plus shutdown grace (10s). See
[Health and lifecycle](health-and-lifecycle.md).

## Configuration knobs

| Concern | Values keys | Notes |
|---------|-------------|--------|
| Image | `image.*` | Tag defaults to chart `appVersion` |
| grex YAML | `config.*`, `listeners.*` | Rendered into the ConfigMap |
| Env overrides | `extraEnv` | `GREX_*` after file load |
| OpAMP TLS | `tls.*` | Requires `tls.existingSecret` when enabled |
| UI exposure | `ingress.*` or `service.type` | Do not put OpAMP on HTTP Ingress casually |
| Scrapes | `serviceMonitor.*` or external jobs | Two jobs, same telemetry port |
| Gateway | `opampGateway.enabled` | Needs a gateway-capable image |

Full field list: [Helm chart reference](../reference/helm-chart.md).
Application settings: [Configuration](configuration.md).

## Single replica (important)

grex **does not persist fleet state**. Each process holds its own registry.
Running `replicaCount > 1` without sticky sessions and a shared store
**splits the fleet view** across pods. Keep one replica until HA is
designed. The chart’s HPA is off by default and capped at one for the same
reason.

## OpAMP gateway (optional)

For large fleets, enable the optional gateway Deployment so agents dial the
gateway and the gateway multiplexes upstream to grex:

```yaml
opampGateway:
  enabled: true
  image:
    repository: ghcr.io/dennisme/grex-opamp-gateway
    tag: "0.1.0"
  # Supply a full collector config, or use the chart’s minimal default:
  # config: |
  #   extensions:
  #     opampgateway: ...
```

The default gateway config is a starting point only. Production configs
should match your collector distribution and the patterns in
[Scaling with gateways](scaling-with-gateways.md) and
`deploy/compose/opamp-gateway.yaml`.

## Scraping metrics

Prefer Prometheus Operator ServiceMonitors:

```yaml
serviceMonitor:
  enabled: true
```

That creates **two** ServiceMonitors (server `/metrics`, fleet
`/metrics/fleet`) with independent intervals, matching
[Scraping](../observability/scraping.md).

Without the operator, either set `metrics.serviceAnnotations.enabled=true`
for a single `/metrics` annotation scrape, or point static scrape configs
at the telemetry Service port with **two jobs**.

## Security checklist

- **UI/API is open** until authentication ships
  ([issue #11](https://github.com/dennisme/grex/issues/11)). Restrict with
  network policy, mesh, or Ingress auth.
- Prefer **mTLS on OpAMP** (`tls.enabled` + client CA) for any non-lab fleet.
- Keep the **telemetry** port off the public internet (especially if
  `config.debug.pprof_enabled` is true).
- Pod defaults: non-root, read-only root filesystem, dropped capabilities.

## Upgrade and uninstall

```sh
helm repo update
helm upgrade grex grex/grex -n grex -f grex-values.yaml

helm uninstall grex -n grex
# Secrets you created (TLS) are not removed by the chart.
```

ConfigMap changes roll the Deployment via a content checksum annotation.

## Troubleshooting

| Symptom | Check |
|---------|--------|
| Pod `CrashLoopBackOff` | `kubectl -n grex logs deploy/grex` — often missing TLS files or bad YAML |
| Image pull errors | Tag/registry, `imagePullSecrets`, or load local image into the node |
| Empty fleet UI | Collectors not reaching OpAMP Service; path must include `/v1/opamp` |
| Ready never true | Readiness is `/readyz` on telemetry; ensure port 9090 is free and probes match |
| ServiceMonitor ignored | Prometheus Operator CRDs missing, or label selectors do not match your Prometheus |

Validate a values file without installing:

```sh
helm template grex grex/grex -f grex-values.yaml | less
helm lint deploy/charts/grex   # from a git checkout
```

## Next steps

- [Helm chart reference](../reference/helm-chart.md) — every values key
- [TLS and mTLS](tls-mtls.md) — certificate layout
- [Configuration](configuration.md) — grex YAML fields
- [Scraping](../observability/scraping.md) — dual Prometheus jobs
- [Compose stack](../getting-started/compose-stack.md) — local multi-collector lab
