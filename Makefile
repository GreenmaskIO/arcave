.PHONY: build demo-media test test-unit test-integration test-verbose test-fidelity test-storage lint clean generate-restic-api

# The restic REST API is generated from its own OpenAPI document; the version is
# pinned because the generated code is committed and a generator bump is a diff.
RESTIC_API_DIR := internal/service/backend/restic/rest
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

# Unit tests need no daemon; the integration suites under test/ need a reachable one.
UNIT_PKGS := ./cmd/... ./internal/... ./pkg/...
INTEGRATION_PKGS := ./test/...
# Concurrent tests per integration package: the daemon's disk, not the CPU, is the bound.
INTEGRATION_PARALLEL := 4
# Extra go test flags for the integration run, e.g. -run or -skip to split it across CI legs.
INTEGRATION_FLAGS ?=

test: test-unit test-integration

test-unit:
	go test $(UNIT_PKGS)

test-integration:
	go test -timeout 30m -parallel $(INTEGRATION_PARALLEL) $(INTEGRATION_FLAGS) $(INTEGRATION_PKGS)

test-verbose:
	go test -v -timeout 30m ./...

# The spec-fidelity catalog brings a compose project up per case against the live daemon:
# minutes, not seconds, so it stays out of `test` behind its build tag.
test-fidelity:
	go test -tags fidelity -run TestIntrospectAssemble -count=1 -timeout 30m ./test/

# The core workflow through the built binary on every storage backend, each against its
# server in a container; part of test-integration too, this runs it alone.
test-storage:
	go test -run TestStorageE2E -count=1 -timeout 20m -parallel $(INTEGRATION_PARALLEL) ./test/

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/

generate-restic-api:
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
	cd $(RESTIC_API_DIR) && oapi-codegen --config=docs/api/openapi-v1-server-config.yaml docs/api/openapi.yaml
	cd $(RESTIC_API_DIR) && oapi-codegen --config=docs/api/openapi-v1-dto-config.yaml docs/api/openapi.yaml
