.PHONY: build test test-verbose lint clean generate-restic-api

# The restic REST API is generated from its own OpenAPI document; the version is
# pinned because the generated code is committed and a generator bump is a diff.
RESTIC_API_DIR := internal/service/backend/restic/rest
OAPI_CODEGEN_VERSION := v2.6.0

build:
	go build -o bin/arcave ./cmd/arcave

test:
	go test ./...

test-verbose:
	go test -v ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/

generate-restic-api:
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
	cd $(RESTIC_API_DIR) && oapi-codegen --config=docs/api/openapi-v1-server-config.yaml docs/api/openapi.yaml
	cd $(RESTIC_API_DIR) && oapi-codegen --config=docs/api/openapi-v1-dto-config.yaml docs/api/openapi.yaml
