FROM golang:1.26-alpine AS build
RUN apk add --no-cache git
RUN go install go.opentelemetry.io/collector/cmd/builder@v0.156.0
WORKDIR /build
COPY manifest.yaml /build/manifest.yaml
# GOPROXY=direct + GOSUMDB=off: the replace target is a commit on a personal
# fork branch, not a tagged release proxy.golang.org has indexed or that the
# checksum database knows about.
RUN GOPROXY=direct GOSUMDB=off CGO_ENABLED=0 builder --config /build/manifest.yaml

FROM alpine:3.22
RUN addgroup -S otel && adduser -S -G otel otel
COPY --from=build /build/out/opamp-gateway /usr/local/bin/opamp-gateway
USER otel
ENTRYPOINT ["/usr/local/bin/opamp-gateway"]
