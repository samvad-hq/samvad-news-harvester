.PHONY: build run test race lint fmt tidy hooks

build:
	go build ./...

run:
	go run ./cmd/harvester

test:
	go test ./...

race:
	go test -race ./...

# The config is v2 schema, which golangci-lint v1 cannot parse, and v1
# also refuses a module targeting Go 1.25. Failing with a clear message
# beats failing with "can't load config".
GOLANGCI_LINT_VERSION := 2.13.2

lint:
	go vet ./...
	@golangci-lint version 2>/dev/null | grep -q ' 2\.' || { \
		echo "golangci-lint v2 is required (this repo pins v$(GOLANGCI_LINT_VERSION))."; \
		echo "install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION)"; \
		exit 1; \
	}
	golangci-lint run

fmt:
	gofmt -w .

tidy:
	go mod tidy

hooks:
	git config core.hooksPath scripts/githooks
