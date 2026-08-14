.PHONY: build test test-verbose lint clean generate generate-restic-api

# The restic REST API is generated from its own OpenAPI document; the version is
# pinned because the generated code is committed and a generator bump is a diff.
RESTIC_API_DIR := internal/provider/docker/internal/backend/restic/rest
OAPI_CODEGEN_VERSION := v2.6.0

build:
	go build -o bin/arcave ./cmd/arcave
	go build -o bin/control-plane ./cmd/control-plane

test:
	go test ./...

test-verbose:
	go test -v ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/

# Every go:generate directive in the tree. The generated code is committed, so
# this is not part of a build: CI runs the same target and fails on a dirty tree,
# which is what keeps the checked-in code and the spec in step.
generate:
	go generate ./...

generate-restic-api:
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
	cd $(RESTIC_API_DIR) && oapi-codegen --config=docs/api/openapi-v1-server-config.yaml docs/api/openapi.yaml
	cd $(RESTIC_API_DIR) && oapi-codegen --config=docs/api/openapi-v1-dto-config.yaml docs/api/openapi.yaml
