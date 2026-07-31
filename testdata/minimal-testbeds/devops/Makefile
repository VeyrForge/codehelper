.PHONY: all build test deps

deps:
	go mod download

build: deps
	go build -o bin/api ./cmd/api

test: build
	go test ./...
