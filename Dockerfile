FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/grex ./cmd/grex

FROM alpine:3.22
RUN addgroup -S grex && adduser -S -G grex grex
COPY --from=build /out/grex /usr/local/bin/grex
USER grex
ENTRYPOINT ["/usr/local/bin/grex"]
