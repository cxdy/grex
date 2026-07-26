# Supervisor image for agent-2 in the compose dev stack: the standalone,
# Cosign-signed opampsupervisor binary (no OCB build needed, it isn't a
# collector distribution) plus the otelcol-contrib binary it manages. See
# docs/spec/design.md's Local development section.
FROM alpine:3.22 AS fetch
RUN apk add --no-cache ca-certificates curl
ARG TARGETARCH
ARG SUPERVISOR_VERSION=0.157.0
RUN curl -fsSL -o /usr/local/bin/opampsupervisor \
      "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/cmd%2Fopampsupervisor%2Fv${SUPERVISOR_VERSION}/opampsupervisor_${SUPERVISOR_VERSION}_linux_${TARGETARCH}" \
    && chmod +x /usr/local/bin/opampsupervisor

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=fetch /usr/local/bin/opampsupervisor /usr/local/bin/opampsupervisor
COPY --from=otel/opentelemetry-collector-contrib:0.157.0 /otelcol-contrib /otelcol-contrib
ENTRYPOINT ["/usr/local/bin/opampsupervisor"]
CMD ["--config", "/etc/otelcol/supervisor.yaml"]
