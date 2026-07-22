.PHONY: build test lint clean

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
