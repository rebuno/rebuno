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
	golangci-lint run ./...

clean:
	rm -rf bin/

tidy:
	go mod tidy

fmt:
	gofmt -s -w .
