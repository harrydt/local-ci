VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test vet fmt check

# The cache server runs inside the ARM64 Linux VM; localci setup copies it there
# from next to the localci binary.
build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/localci ./cmd/localci
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o bin/localci-cache-linux-arm64 ./cmd/localci-cache

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# check fails if any file is not gofmt'd; run before committing.
check: vet test
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "run: make fmt"; exit 1)
