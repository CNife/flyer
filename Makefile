.PHONY: build test vet

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X github.com/CNife/flyer/internal/cli.Version=$(VERSION)" \
		-o dist/flyer ./cmd/flyer

test:
	go test ./...

vet:
	go vet ./...
