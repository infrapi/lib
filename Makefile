TEST?=$$(go list ./...)
GOFMT_FILES?=$$(find . -name '*.go' | grep -vE './_local')
GO_CMD ?= go
SHELL := /bin/bash

.PHONY: setup clean tidy fmt lint security test docs
all: setup clean tidy fmt lint security test

setup:
	@command -v golangci-lint 2>&1 > /dev/null || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	@command -v gosec 2>&1 > /dev/null         || go install github.com/securego/gosec/v2/cmd/gosec@latest
	@command -v goreleaser 2>&1 > /dev/null    || go install github.com/goreleaser/goreleaser@latest

clean:
	rm -rf ./dist

tidy:
	$(GO_CMD) mod tidy

fmt:
	$(GO_CMD)fmt -w $(GOFMT_FILES)

lint:
	golangci-lint run

security:
	gosec -exclude-dir _local -quiet ./...

test:
	$(GO_CMD) test -v -timeout 60s -coverprofile=cover.out -cover $(TEST)
	$(GO_CMD) tool cover -func=cover.out
	$(GO_CMD) tool cover -html=cover.out -o coverage.html

docs: ## Preview these docs in the InfraPI portal (needs infrapi/docs checked out next to this repo)
	@[ -d ../docs ] || (echo "Docs are published by the portal: clone infrapi/docs next to this repo, then re-run make docs" && exit 1)
	@$(MAKE) -C ../docs dev