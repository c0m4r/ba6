# ba6 - single static, dependency-free multicall coreutils binary.
#
# CGO_ENABLED=0 is mandatory, not cosmetic:
#   * it produces a truly static binary (the zero-dependency goal), and
#   * it forces the pure-Go os/user path (reading /etc/passwd directly) so the
#     seccomp socket ban in the hardening layer never trips NSS.
# A plain `go build` on a host with a C toolchain defaults to CGO_ENABLED=1 and
# would silently produce a dynamic, libc-linked binary, so always build via this
# Makefile (or export CGO_ENABLED=0 yourself).

BINARY      := ba6
GOPATH_BIN  := $(shell go env GOPATH)/bin
LDFLAGS     := -s -w
BUILD_ENV   := CGO_ENABLED=0 GOOS=linux GOARCH=amd64
GO_BUILD_FLAGS := -buildvcs=false

.PHONY: all build lint vet fmt fmt-check test verify clean

all: build

build:
	$(BUILD_ENV) go build $(GO_BUILD_FLAGS) -ldflags='$(LDFLAGS)' -trimpath -o $(BINARY) .
	@file $(BINARY) | grep -q 'statically linked' \
		&& echo "ok: $(BINARY) is statically linked" \
		|| { echo "ERROR: $(BINARY) is not statically linked"; exit 1; }

vet:
	go vet ./...

fmt:
	gofmt -w *.go

fmt-check:
	@test -z "$$(gofmt -l *.go)" || { gofmt -l *.go; exit 1; }

test:
	go test ./...

# golangci-lint (with gosec) lives in GOPATH/bin, which may not be on PATH.
lint:
	PATH="$(PATH):$(GOPATH_BIN)" golangci-lint run ./...

# Aggregate gate: formatting, vet, and the full linter incl. gosec.
verify: fmt-check test vet lint build
	@echo "all checks passed"

clean:
	rm -f $(BINARY)
