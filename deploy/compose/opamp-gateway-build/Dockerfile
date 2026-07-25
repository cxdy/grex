FROM golang:1.26-alpine AS build
RUN go install go.opentelemetry.io/collector/cmd/builder@v0.156.0
WORKDIR /build
RUN go mod init grex-opamp-gateway-build \
    && go mod download github.com/observiq/bindplane-otel-contrib/extension/opampgateway@v1.10.0 \
    && cp -r "$(go env GOMODCACHE)/github.com/observiq/bindplane-otel-contrib/extension/opampgateway@v1.10.0" /build/opampgateway \
    && chmod -R u+w /build/opampgateway
COPY patch-client-tls.sh /build/
RUN sh /build/patch-client-tls.sh /build/opampgateway
COPY manifest.yaml /build/manifest.yaml
RUN CGO_ENABLED=0 builder --config /build/manifest.yaml

FROM alpine:3.22
RUN addgroup -S otel && adduser -S -G otel otel
COPY --from=build /build/out/opamp-gateway /usr/local/bin/opamp-gateway
USER otel
ENTRYPOINT ["/usr/local/bin/opamp-gateway"]
