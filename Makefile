TEST?=$$(go list ./...)
GOFMT_FILES?=$$(find . -name '*.go' | grep -vE './_local')
GO_CMD ?= go
SHELL := /bin/bash

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

docs:
	@echo "Building documentation with MkDocs..."
	@command -v mkdocs >/dev/null 2>&1 || (echo "Error: mkdocs not found. Install with: pip install mkdocs-material mkdocs-git-revision-date-localized-plugin" && exit 1)
	@mkdocs build
	@echo "Documentation built in site/"
	@echo "To serve locally, run: make docs-serve"

docs-serve:
	@echo "Serving documentation at http://localhost:8000"
	@command -v mkdocs >/dev/null 2>&1 || (echo "Error: mkdocs not found. Install with: pip install mkdocs-material mkdocs-git-revision-date-localized-plugin" && exit 1)
	@mkdocs serve

docs-deploy:
	@echo "Deploying documentation to GitHub Pages..."
	@command -v mkdocs >/dev/null 2>&1 || (echo "Error: mkdocs not found. Install with: pip install mkdocs-material mkdocs-git-revision-date-localized-plugin" && exit 1)
	@mkdocs gh-deploy --force
