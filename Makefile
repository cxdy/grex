.PHONY: build test lint compose-up compose-down

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/dennisme/grex/internal/buildinfo.Version=$(VERSION) \
           -X github.com/dennisme/grex/internal/buildinfo.Commit=$(COMMIT) \
           -X github.com/dennisme/grex/internal/buildinfo.Date=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" ./...

test:
	go test -race ./...

lint:
	golangci-lint run

compose-up:
	docker compose up -d --build --wait

compose-down:
	docker compose down -v
