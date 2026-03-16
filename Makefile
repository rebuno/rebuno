.PHONY: build dev test test-integration lint clean docker-build docker-up docker-down demo demo-client tidy fmt

VERSION ?= dev

build:
	go build -ldflags "-X main.Version=$(VERSION)" -o bin/rebuno ./cmd/rebuno

dev:
	make build
	bin/rebuno dev

test:
	go test -race ./...

test-integration:
	go test -tags integration -count=1 -v ./tests/integration/...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/

tidy:
	go mod tidy

fmt:
	gofmt -s -w .
