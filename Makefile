.PHONY: build test lint compose-up compose-down

build:
	go build ./...

test:
	go test -race ./...

lint:
	golangci-lint run

compose-up:
	docker compose up -d --build --wait

compose-down:
	docker compose down -v
