.PHONY: help test fmt vet lint audit check

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "}; /^[a-zA-Z_-]+:.*?## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: ## Run all tests
	go test -race -coverprofile=coverage.txt -covermode=atomic -v ./...

fmt: ## Run code formatting
	gofmt -w .

vet: ## Run static code analysis
	go vet ./...

lint: ## Run linters
	golangci-lint run

audit: ## Run vulnerability scan
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: fmt vet lint test audit ## Run all checks: formatting, vet, lint, tests, and audit
