.PHONY: build demo-media docs-install docs-serve docs-build test test-unit test-integration test-verbose test-fidelity test-storage lint lint-e2e-imports clean generate-restic-api

# The restic REST API is generated from its own OpenAPI document; the version is
# pinned because the generated code is committed and a generator bump is a diff.
RESTIC_API_DIR := internal/provider/compose/internal/service/volumebackend/cdc/rest
OAPI_CODEGEN_VERSION := v2.6.0

# The build stamps cmd/arcave/cmd_root.go declares; goreleaser fills the same vars in CI.
# Prefixed so an unrelated VERSION or COMMIT in the environment is never stamped in.
ARCAVE_VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
ARCAVE_COMMIT     ?= $(shell git rev-parse HEAD 2>/dev/null || echo none)
ARCAVE_BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(ARCAVE_VERSION) -X main.commit=$(ARCAVE_COMMIT) -X main.buildDate=$(ARCAVE_BUILD_DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/arcave ./cmd/arcave

# The docs' terminal recordings (docs/assets/demo/), re-recorded with vhs against a
# scratch copy of examples/demo; TAPE=base-cycle records one.
demo-media:
	examples/demo/tapes/record.sh $(TAPE)

# The docs/ tree rendered with the stock Docusaurus theme; the published site has its own
# config and styles.
DOCS_PREVIEW_DIR := tools/docs-preview

docs-install:
	npm --prefix $(DOCS_PREVIEW_DIR) ci

docs-serve: docs-install
	npm --prefix $(DOCS_PREVIEW_DIR) run start

docs-build: docs-install
	npm --prefix $(DOCS_PREVIEW_DIR) run build

# Unit tests need no daemon; the e2e suite under test/ and the compose provider's integration suite
# need a reachable one.
E2E_DIR := ./test
INTEGRATION_DIR := ./internal/provider/compose/test
UNIT_PKGS = $(shell go list ./cmd/... ./internal/... ./pkg/... | grep -Ev '/internal/provider/[^/]+/test(/|$$)')
INTEGRATION_PKGS := $(E2E_DIR)/... $(INTEGRATION_DIR)/...
# Concurrent tests per integration package: the daemon's disk, not the CPU, is the bound.
INTEGRATION_PARALLEL := 4
# Extra go test flags for the integration run, e.g. -run or -skip to split it across CI legs.
INTEGRATION_FLAGS ?=

test: test-unit test-integration

test-unit:
	@test -n "$(UNIT_PKGS)" || { echo 'no unit packages: go list failed' >&2; exit 1; }
	go test $(UNIT_PKGS)

# -count=1: the e2e suite drives the built binary, which no test cache entry depends on.
test-integration:
	go test -count=1 -timeout 30m -parallel $(INTEGRATION_PARALLEL) $(INTEGRATION_FLAGS) $(INTEGRATION_PKGS)

test-verbose:
	go test -v -timeout 30m ./...

# The spec-fidelity catalog brings a compose project up per case against the live daemon:
# minutes, not seconds, so it stays out of `test` behind its build tag.
test-fidelity:
	go test -tags fidelity -run TestIntrospectAssemble -count=1 -timeout 30m $(INTEGRATION_DIR)

# The core workflow through the built binary on every storage backend, each against its
# server in a container; part of test-integration too, this runs it alone.
test-storage:
	go test -run TestStorageE2E -count=1 -timeout 20m -parallel $(INTEGRATION_PARALLEL) $(E2E_DIR)

# What the e2e suite may import of arcave: it drives the built binary, never a service, client or
# provider of its own.
E2E_ALLOWED_IMPORTS := internal/(model|config|provider/compose/config|storefactory|repository/[a-z]+|service/lock|utils/testutils/daemontest)|pkg/.+

lint: lint-e2e-imports
	golangci-lint run ./...

lint-e2e-imports:
	@leaked=$$(go list -f '{{join .Imports "\n"}}{{"\n"}}{{join .TestImports "\n"}}{{"\n"}}{{join .XTestImports "\n"}}' $(E2E_DIR)/... | grep 'greenmaskio/arcave/' | \
		grep -Ev 'greenmaskio/arcave/($(E2E_ALLOWED_IMPORTS))$$' | sort -u); \
	if [ -n "$$leaked" ]; then echo "test/ imports what the e2e suite may not: $$leaked" >&2; exit 1; fi

clean:
	rm -rf bin/

generate-restic-api:
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
	cd $(RESTIC_API_DIR) && oapi-codegen --config=docs/api/openapi-v1-server-config.yaml docs/api/openapi.yaml
	cd $(RESTIC_API_DIR) && oapi-codegen --config=docs/api/openapi-v1-dto-config.yaml docs/api/openapi.yaml
