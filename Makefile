.PHONY: build run test tidy format hooks

build:
	go build ./...

run:
	go run ./cmd/collector

test:
	go test ./...

tidy:
	go mod tidy

format:
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

hooks:
	git config core.hooksPath scripts/githooks

lint:
	go vet ./...
