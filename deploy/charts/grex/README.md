# grex Helm chart

Kubernetes package for [grex](https://github.com/dennisme/grex), an OpAMP
control plane for OpenTelemetry Collector fleets.

## Install from the chart repository (GitHub Pages)

The chart is published under the project docs site at a dedicated `/charts/`
path so it does not collide with MkDocs pages or the static demo:

```sh
helm repo add grex https://dennisme.github.io/grex/charts/
helm repo update
helm install grex grex/grex --namespace grex --create-namespace
```

## Install from a local checkout

```sh
helm install grex ./deploy/charts/grex --namespace grex --create-namespace
```

## Documentation

- [Deploy with Helm](https://dennisme.github.io/grex/admin/helm/) — how-to
- [Helm chart reference](https://dennisme.github.io/grex/reference/helm-chart/) — values
- [Install](https://dennisme.github.io/grex/admin/install/) — all install methods

## What this chart deploys

| Resource | Purpose |
|----------|---------|
| Deployment + Service | grex with named ports `opamp`, `ui`, `telemetry` |
| ConfigMap | grex YAML config |
| optional Secret mount | OpAMP TLS / mTLS PEMs (`tls.existingSecret`) |
| optional Ingress | UI / JSON API only |
| optional ServiceMonitors | dual scrape jobs for `/metrics` and `/metrics/fleet` |
| optional OpAMP gateway | connection multiplexing (`opampGateway.enabled`) |

Fleet state is **in memory**. Keep `replicaCount: 1` until HA is designed.
