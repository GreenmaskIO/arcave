.PHONY: build test lint clean

build:
	go build -o bin/gm ./cmd/gm

test:
	go test ./...

test-verbose:
	go test -v ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/
