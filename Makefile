.PHONY: build dev test lint clean tidy fmt

VERSION ?= dev

build:
	go build -ldflags "-X main.Version=$(VERSION)" -o bin/rebuno ./cmd/rebuno

dev:
	make build
	bin/rebuno dev

test:
	go test -race ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...

clean:
	rm -rf bin/

tidy:
	go mod tidy

fmt:
	gofmt -s -w .
