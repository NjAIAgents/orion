# Orion build. No external Go dependencies: go.sum stays empty and the
# binary builds offline.

BINARY  := orion
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.Version=$(VERSION)
PREFIX  ?= $(HOME)/.local

.PHONY: all build test vet fmt install clean release-dirs dist

all: vet test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/orion

# Run this first on a fresh clone. Nothing in this repo has been compiled
# by its author's environment, so the test suite is the acceptance gate.
test:
	go test ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -l -w .

install: build
	install -d $(PREFIX)/bin
	install -m 0755 bin/$(BINARY) $(PREFIX)/bin/$(BINARY)
	@echo "installed to $(PREFIX)/bin/$(BINARY)"
	@command -v $(BINARY) >/dev/null || echo "note: $(PREFIX)/bin is not on your PATH"

# Cross-compile every supported target. CGO is off, so this needs no
# platform toolchains.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

dist:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=""; \
	  [ "$$os" = "windows" ] && ext=".exe"; \
	  echo "building $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
	    -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-$$os-$$arch$$ext ./cmd/orion; \
	done
	@cd dist && shasum -a 256 * > SHA256SUMS 2>/dev/null || sha256sum * > SHA256SUMS

clean:
	rm -rf bin dist
