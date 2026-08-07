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

.PHONY: all build lint vet fmt fmt-check test provenance coverage verify clean

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

# Fails if any applet's help text reuses wording from the original's man page.
# Reads man pages only; it never runs a tool to ask for its help, because the
# applet list contains halt, poweroff, reboot and init. See PROVENANCE.md.
provenance:
	@python3 tools/coverage/text_overlap.py

# Regenerate the per-applet comparison behind COVERAGE.md.
coverage: build
	@cd tools/coverage \
		&& python3 ref_options.py > ref.json \
		&& python3 ba6_options.py > ba6.json \
		&& python3 compare.py ref.json ba6.json

# Aggregate gate: formatting, vet, the full linter incl. gosec, and the
# verbatim-text check.
verify: fmt-check test vet lint build provenance
	@echo "all checks passed"

clean:
	rm -f $(BINARY)
