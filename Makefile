VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test vet fmt check

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/localci ./cmd/localci

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# check fails if any file is not gofmt'd; run before committing.
check: vet test
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "run: make fmt"; exit 1)
