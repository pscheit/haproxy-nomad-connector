# haproxy-nomad-connector Makefile

.PHONY: build test clean lint fmt vet coverage help install deps

# Build variables
BINARY_NAME=haproxy-nomad-connector
BUILD_DIR=./build
CMD_DIR=./cmd/haproxy-nomad-connector
VERSION?=$(shell git describe --tags --always --dirty)
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%I:%M:%S%p')
COMMIT=$(shell git rev-parse HEAD)

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=gofmt

# Build flags
LDFLAGS=-ldflags "-s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.Commit=$(COMMIT)"

## help: Show this help message
help:
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Targets:'
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'

## build: Build the binary
build: deps
	mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)

## build-all: Build for all platforms
build-all: deps
	mkdir -p $(BUILD_DIR)
	# Linux amd64
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(CMD_DIR)
	# Linux arm64
	GOOS=linux GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 $(CMD_DIR)
	# macOS amd64
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(CMD_DIR)
	# macOS arm64
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(CMD_DIR)
	# Windows amd64
	GOOS=windows GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(CMD_DIR)

## install: Install the binary to GOPATH/bin
install: deps
	$(GOCMD) install $(LDFLAGS) $(CMD_DIR)

## test: Run tests
test: deps
	$(GOTEST) -v -race ./cmd/... ./internal/...

## test-integration: Run integration tests  
test-integration: deps
	$(GOTEST) -v -race -tags=integration ./e2e/...

## test-coverage: Run tests with coverage
test-coverage: deps
	$(GOTEST) -v -race -coverprofile=coverage.out -covermode=atomic ./cmd/... ./internal/...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

## bench: Run benchmarks
bench: deps
	$(GOTEST) -bench=. -benchmem ./cmd/... ./internal/...

# Keep in sync with .github/workflows/ci.yml (golangci-lint-action version).
# Pinned so local lint matches CI and neither drifts onto a newer release.
GOLANGCI_LINT_VERSION=2.11.3

## lint: Run linter (pinned version, must match CI)
lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint not installed. Install v$(GOLANGCI_LINT_VERSION): https://golangci-lint.run/docs/welcome/install/#binaries"; \
		exit 1; \
	fi
	@golangci-lint version 2>&1 | grep -q "$(GOLANGCI_LINT_VERSION)" || \
		echo "WARNING: local golangci-lint differs from the pinned v$(GOLANGCI_LINT_VERSION) used by CI — results may not match"
	golangci-lint run ./...

## fmt: Format code
fmt:
	$(GOFMT) -s -w $$(find . -name '*.go' -not -path './dev/*' 2>/dev/null)

## vet: Run go vet
vet: deps
	$(GOCMD) vet ./...

## deps: Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) verify

## tidy: Tidy dependencies
tidy:
	$(GOMOD) tidy

## clean: Clean build artifacts
clean:
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

## check: Run all checks (fmt, vet, lint, test)
check: fmt vet lint test

## dev: Start development environment
dev: deps build
	./$(BUILD_DIR)/$(BINARY_NAME) --help

## docker-build: Build Docker image
docker-build:
	docker build -t $(BINARY_NAME):$(VERSION) .
	docker tag $(BINARY_NAME):$(VERSION) $(BINARY_NAME):latest

.DEFAULT_GOAL := help