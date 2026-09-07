.PHONY: build run test race lint fmt tidy hooks

build:
	go build ./...

run:
	go run ./cmd/harvester

test:
	go test ./...

race:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run

fmt:
	gofmt -w .

tidy:
	go mod tidy

hooks:
	git config core.hooksPath scripts/githooks
