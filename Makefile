TEST?=$$(go list ./...)
GOFMT_FILES?=$$(find . -name '*.go' | grep -vE './_local')
GO_CMD ?= go
SHELL := /bin/bash

# go test, golangci-lint and gosec all stop at a module boundary, so every
# target below has to walk the modules itself. pkg/cluster, pkg/app and their
# examples are modules of their own to keep dskit out of the root dependency
# tree.
MODULES ?= . pkg/cluster pkg/app examples/cluster examples/app

.PHONY: setup clean tidy fmt lint security test verify-submodules docs
all: setup clean tidy fmt lint security test

setup:
	@command -v golangci-lint 2>&1 > /dev/null || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	@command -v gosec 2>&1 > /dev/null         || go install github.com/securego/gosec/v2/cmd/gosec@latest
	@command -v goreleaser 2>&1 > /dev/null    || go install github.com/goreleaser/goreleaser@latest

clean:
	rm -rf ./dist

tidy:
	@for module in $(MODULES); do \
		echo "==> tidy $$module"; \
		(cd $$module && $(GO_CMD) mod tidy) || exit 1; \
	done

fmt:
	$(GO_CMD)fmt -w $(GOFMT_FILES)

lint:
	@for module in $(MODULES); do \
		echo "==> lint $$module"; \
		(cd $$module && golangci-lint run) || exit 1; \
	done

security:
	@for module in $(MODULES); do \
		echo "==> security $$module"; \
		(cd $$module && gosec -exclude-dir _local -quiet ./...) || exit 1; \
	done

# The root module keeps its 60s budget, most of which pkg/http spends on real
# retry backoff. The cluster module gets its own, because it starts real gossip
# clusters and waits for them to converge.
test:
	$(GO_CMD) test -v -timeout 60s -coverprofile=cover.out -cover $(TEST)
	$(GO_CMD) tool cover -func=cover.out
	$(GO_CMD) tool cover -html=cover.out -o coverage.html
	# The report has to run inside the module that owns the profile: from here,
	# `go tool cover` cannot resolve the cluster module's packages.
	cd pkg/cluster && \
		$(GO_CMD) test -v -timeout 120s -coverprofile=cover.out -cover ./... && \
		$(GO_CMD) tool cover -func=cover.out && \
		$(GO_CMD) tool cover -html=cover.out -o ../../coverage-cluster.html
	cd pkg/app && \
		$(GO_CMD) test -v -timeout 120s -coverprofile=cover.out -cover ./... && \
		$(GO_CMD) tool cover -func=cover.out && \
		$(GO_CMD) tool cover -html=cover.out -o ../../coverage-app.html

# A replace directive keeps the submodules building against this working tree,
# which also means a broken require line would never be noticed. This builds
# them the way a consumer does: without the replace, against the published
# parent.
#
# It is deliberately NOT part of `all`, and not in CI yet: it cannot pass until
# a tag of the parent carries the INFRAPI_CLUSTER_* keys. Wire it into both once
# that tag exists, and the require line can never silently rot again.
verify-submodules:
	@for module in pkg/cluster pkg/app examples/cluster examples/app; do \
		echo "==> verify $$module against the published parent"; \
		rm -rf /tmp/infrapi-verify && mkdir -p /tmp/infrapi-verify; \
		cp -r $$module/. /tmp/infrapi-verify/; \
		grep -v '=> \.\.' /tmp/infrapi-verify/go.mod > /tmp/infrapi-verify/go.mod.new; \
		mv /tmp/infrapi-verify/go.mod.new /tmp/infrapi-verify/go.mod; \
		(cd /tmp/infrapi-verify && $(GO_CMD) build ./...) || exit 1; \
	done
	@rm -rf /tmp/infrapi-verify

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
