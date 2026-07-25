#!/usr/bin/env bash
# End-to-end smoke test for the grex Helm chart.
#
# Builds the grex image from the repo Dockerfile, loads it into a local
# Kubernetes cluster (kind or k3d), installs the chart, and asserts health,
# readiness, metrics, the JSON API, and `helm test`.
#
# Usage (from repo root):
#   deploy/charts/smoke.sh              # auto-pick kind or k3d; create cluster if needed
#   deploy/charts/smoke.sh --provider kind
#   deploy/charts/smoke.sh --provider k3d
#   deploy/charts/smoke.sh --keep       # leave cluster + release after success
#   deploy/charts/smoke.sh --skip-build # reuse already-built grex:e2e image
#
# Environment:
#   CLUSTER_PROVIDER   kind | k3d (default: first available)
#   CLUSTER_NAME       default: grex-e2e
#   RELEASE            default: grex
#   NAMESPACE          default: grex-e2e
#   IMAGE              default: grex:e2e
#   KEEP               1 to skip teardown (same as --keep)
#   SKIP_BUILD         1 to skip docker build (same as --skip-build)
#   CREATE_CLUSTER     0 to require an existing cluster (default: 1)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

CHART="$ROOT/deploy/charts/grex"
VALUES="$ROOT/deploy/charts/ci/values-e2e.yaml"

CLUSTER_PROVIDER="${CLUSTER_PROVIDER:-}"
CLUSTER_NAME="${CLUSTER_NAME:-grex-e2e}"
RELEASE="${RELEASE:-grex}"
NAMESPACE="${NAMESPACE:-grex-e2e}"
IMAGE="${IMAGE:-grex:e2e}"
KEEP="${KEEP:-0}"
SKIP_BUILD="${SKIP_BUILD:-0}"
CREATE_CLUSTER="${CREATE_CLUSTER:-1}"

CREATED_CLUSTER=0
INSTALLED_RELEASE=0
PF_PID=""

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [ $# -gt 0 ]; do
  case "$1" in
  --provider)
    CLUSTER_PROVIDER="$2"
    shift 2
    ;;
  --provider=*)
    CLUSTER_PROVIDER="${1#*=}"
    shift
    ;;
  --keep)
    KEEP=1
    shift
    ;;
  --skip-build)
    SKIP_BUILD=1
    shift
    ;;
  --no-create)
    CREATE_CLUSTER=0
    shift
    ;;
  -h | --help)
    usage 0
    ;;
  *)
    echo "unknown argument: $1" >&2
    usage 1
    ;;
  esac
done

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

info() {
  echo "==> $*"
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

pick_provider() {
  if [ -n "$CLUSTER_PROVIDER" ]; then
    case "$CLUSTER_PROVIDER" in
    kind | k3d) ;;
    *)
      fail "unsupported provider: $CLUSTER_PROVIDER (want kind or k3d)"
      ;;
    esac
    return
  fi
  if command -v kind >/dev/null 2>&1; then
    CLUSTER_PROVIDER=kind
  elif command -v k3d >/dev/null 2>&1; then
    CLUSTER_PROVIDER=k3d
  else
    fail "neither kind nor k3d found; install one or set --provider"
  fi
}

cluster_exists() {
  case "$CLUSTER_PROVIDER" in
  kind)
    kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"
    ;;
  k3d)
    k3d cluster list 2>/dev/null | awk 'NR>1 {print $1}' | grep -qx "$CLUSTER_NAME"
    ;;
  esac
}

create_cluster() {
  if cluster_exists; then
    info "using existing $CLUSTER_PROVIDER cluster $CLUSTER_NAME"
    return
  fi
  [ "$CREATE_CLUSTER" = 1 ] || fail "cluster $CLUSTER_NAME does not exist and CREATE_CLUSTER=0"
  info "creating $CLUSTER_PROVIDER cluster $CLUSTER_NAME"
  case "$CLUSTER_PROVIDER" in
  kind)
    kind create cluster --name "$CLUSTER_NAME" --wait 120s
    ;;
  k3d)
    # Single-server k3s via k3d (Kubernetes API compatible with kubectl/helm).
    k3d cluster create "$CLUSTER_NAME" --wait
    ;;
  esac
  CREATED_CLUSTER=1
}

use_kubeconfig() {
  case "$CLUSTER_PROVIDER" in
  kind)
    kubectl config use-context "kind-${CLUSTER_NAME}" >/dev/null
    ;;
  k3d)
    kubectl config use-context "k3d-${CLUSTER_NAME}" >/dev/null
    ;;
  esac
  kubectl cluster-info >/dev/null
}

image_repo() {
  echo "${IMAGE%%:*}"
}

image_tag() {
  case "$IMAGE" in
  *:*) echo "${IMAGE#*:}" ;;
  *) echo "latest" ;;
  esac
}

build_image() {
  if [ "$SKIP_BUILD" = 1 ]; then
    info "skipping image build (SKIP_BUILD=1)"
    docker image inspect "$IMAGE" >/dev/null 2>&1 || fail "image $IMAGE not found locally"
    return
  fi
  info "building $IMAGE from Dockerfile"
  docker build \
    --build-arg "VERSION=e2e" \
    --build-arg "COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo none)" \
    --build-arg "DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -t "$IMAGE" \
    "$ROOT"
}

load_image() {
  info "loading $IMAGE into $CLUSTER_PROVIDER cluster $CLUSTER_NAME"
  case "$CLUSTER_PROVIDER" in
  kind)
    kind load docker-image "$IMAGE" --name "$CLUSTER_NAME"
    ;;
  k3d)
    k3d image import "$IMAGE" -c "$CLUSTER_NAME"
    ;;
  esac
}

install_chart() {
  info "installing chart release $RELEASE in namespace $NAMESPACE"
  helm upgrade --install "$RELEASE" "$CHART" \
    --namespace "$NAMESPACE" \
    --create-namespace \
    --wait \
    --timeout 3m \
    -f "$VALUES" \
    --set "image.repository=$(image_repo)" \
    --set "image.tag=$(image_tag)" \
    --set image.pullPolicy=Never
  INSTALLED_RELEASE=1
}

wait_ready() {
  info "waiting for deployment rollout"
  kubectl -n "$NAMESPACE" rollout status "deployment/${RELEASE}" --timeout=180s
  kubectl -n "$NAMESPACE" wait --for=condition=ready "pod" \
    -l "app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=server" \
    --timeout=120s
}

stop_port_forward() {
  if [ -n "${PF_PID}" ] && kill -0 "$PF_PID" 2>/dev/null; then
    kill "$PF_PID" 2>/dev/null || true
    wait "$PF_PID" 2>/dev/null || true
  fi
  PF_PID=""
}

# Port-forward UI + telemetry to localhost for host-side curl checks.
start_port_forward() {
  stop_port_forward
  local local_ui=18080
  local local_telemetry=19090
  # Pick free local ports if defaults are busy.
  if command -v python3 >/dev/null 2>&1; then
    eval "$(
      python3 - <<'PY'
import socket
def free():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p
print(f"local_ui={free()}")
print(f"local_telemetry={free()}")
PY
    )"
  fi
  LOCAL_UI="$local_ui"
  LOCAL_TELEMETRY="$local_telemetry"
  info "port-forward svc/${RELEASE} → 127.0.0.1:${LOCAL_UI} (ui), 127.0.0.1:${LOCAL_TELEMETRY} (telemetry)"
  kubectl -n "$NAMESPACE" port-forward "svc/${RELEASE}" \
    "${LOCAL_UI}:8080" "${LOCAL_TELEMETRY}:9090" \
    >/tmp/grex-helm-smoke-pf.log 2>&1 &
  PF_PID=$!
  # Wait until healthz answers through the forward.
  local i body
  for i in $(seq 1 30); do
    if body=$(curl -fsS --max-time 2 "http://127.0.0.1:${LOCAL_TELEMETRY}/healthz" 2>/dev/null); then
      echo "$body" | grep -q ok && return 0
    fi
    kill -0 "$PF_PID" 2>/dev/null || fail "port-forward exited early; see /tmp/grex-helm-smoke-pf.log"
    sleep 1
  done
  fail "port-forward never became ready; see /tmp/grex-helm-smoke-pf.log"
}

assert_http() {
  local url="$1"
  local grep_pat="${2:-}"
  local body
  body=$(curl -fsS --max-time 10 "$url") || fail "GET $url"
  if [ -n "$grep_pat" ]; then
    echo "$body" | grep -Eq "$grep_pat" || fail "GET $url: expected /$grep_pat/, body: ${body:0:200}"
  fi
  echo "ok  $url"
}

run_assertions() {
  start_port_forward

  info "asserting grex endpoints"
  assert_http "http://127.0.0.1:${LOCAL_TELEMETRY}/healthz" 'ok'
  assert_http "http://127.0.0.1:${LOCAL_TELEMETRY}/readyz" 'ok'
  assert_http "http://127.0.0.1:${LOCAL_TELEMETRY}/metrics" 'go_'
  # Fleet endpoint is always registered; series may be sparse with zero agents.
  assert_http "http://127.0.0.1:${LOCAL_TELEMETRY}/metrics/fleet" 'grex_|# HELP|# TYPE'
  assert_http "http://127.0.0.1:${LOCAL_UI}/api/status" '"fleet"'
  assert_http "http://127.0.0.1:${LOCAL_UI}/api/agents" '"agents"'
  assert_http "http://127.0.0.1:${LOCAL_UI}/" '.'

  stop_port_forward

  info "running helm test"
  helm test "$RELEASE" --namespace "$NAMESPACE" --timeout 2m

  info "pod is Running and Ready"
  phase=$(kubectl -n "$NAMESPACE" get pod \
    -l "app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=server" \
    -o jsonpath='{.items[0].status.phase}')
  [ "$phase" = "Running" ] || fail "pod phase want Running, got $phase"
}

cleanup() {
  local code=$?
  stop_port_forward
  if [ "$KEEP" = 1 ]; then
    info "KEEP=1: leaving release $RELEASE (ns $NAMESPACE) and cluster $CLUSTER_NAME"
    exit "$code"
  fi
  if [ "$INSTALLED_RELEASE" = 1 ]; then
    info "uninstalling release $RELEASE"
    helm uninstall "$RELEASE" --namespace "$NAMESPACE" >/dev/null 2>&1 || true
    kubectl delete namespace "$NAMESPACE" --wait=false >/dev/null 2>&1 || true
  fi
  if [ "$CREATED_CLUSTER" = 1 ]; then
    info "deleting $CLUSTER_PROVIDER cluster $CLUSTER_NAME"
    case "$CLUSTER_PROVIDER" in
    kind) kind delete cluster --name "$CLUSTER_NAME" >/dev/null 2>&1 || true ;;
    k3d) k3d cluster delete "$CLUSTER_NAME" >/dev/null 2>&1 || true ;;
    esac
  fi
  exit "$code"
}

main() {
  need_cmd docker
  need_cmd kubectl
  need_cmd helm
  need_cmd curl
  docker info >/dev/null 2>&1 || fail "docker daemon not reachable"

  pick_provider
  need_cmd "$CLUSTER_PROVIDER"

  [ -f "$CHART/Chart.yaml" ] || fail "chart not found at $CHART"
  [ -f "$VALUES" ] || fail "values file not found at $VALUES"

  trap cleanup EXIT

  create_cluster
  use_kubeconfig
  build_image
  load_image
  install_chart
  wait_ready
  run_assertions

  info "helm chart smoke test passed ($CLUSTER_PROVIDER)"
}

main
