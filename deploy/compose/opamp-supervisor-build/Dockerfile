# Supervisor image for agent-2 in the compose dev stack: normally the
# standalone, Cosign-signed opampsupervisor binary (no OCB build needed, it
# isn't a collector distribution) plus the otelcol-contrib binary it
# manages. See docs/spec/design.md's Local development section.
#
# TEMPORARY: built from source off a fork branch instead of pulling the
# signed official release below, to exercise docs/spec/design.md's
# supervisor_managed known-gap fix
# (dennisme/opentelemetry-collector-contrib@dennisme/opamp-supervisor-managed-by,
# adds a declared `opamp.managed_by` non-identifying attribute) before it's
# upstreamed, reviewed, and released. Pinned to a commit, not the branch
# tip, so a later force-push to that branch doesn't silently change what
# this builds — bump SUPERVISOR_FORK_COMMIT by hand if more commits get
# pushed while iterating with maintainers. Revert once the fix ships in an
# official opampsupervisor release: delete this stage, uncomment the
# curl-a-release stage below, bump SUPERVISOR_VERSION to that release.
FROM golang:1.25-alpine AS fetch
RUN apk add --no-cache git ca-certificates
ARG SUPERVISOR_FORK_COMMIT=6b5de746168d0530448c2fcf93a8086fa9ca9c90
WORKDIR /src
RUN git init -q && \
    git remote add origin https://github.com/dennisme/opentelemetry-collector-contrib.git && \
    git fetch --depth 1 -q origin "${SUPERVISOR_FORK_COMMIT}" && \
    git checkout -q FETCH_HEAD
ARG TARGETARCH
RUN cd cmd/opampsupervisor && \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /usr/local/bin/opampsupervisor .

# The signed-release form this temporarily replaces:
#
# FROM alpine:3.22 AS fetch
# RUN apk add --no-cache ca-certificates curl
# ARG TARGETARCH
# ARG SUPERVISOR_VERSION=0.157.0
# RUN curl -fsSL -o /usr/local/bin/opampsupervisor \
#       "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/cmd%2Fopampsupervisor%2Fv${SUPERVISOR_VERSION}/opampsupervisor_${SUPERVISOR_VERSION}_linux_${TARGETARCH}" \
#     && chmod +x /usr/local/bin/opampsupervisor

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=fetch /usr/local/bin/opampsupervisor /usr/local/bin/opampsupervisor
COPY --from=otel/opentelemetry-collector-contrib:0.157.0 /otelcol-contrib /otelcol-contrib
ENTRYPOINT ["/usr/local/bin/opampsupervisor"]
CMD ["--config", "/etc/otelcol/supervisor.yaml"]
