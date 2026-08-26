# Orion build. No external Go dependencies: go.sum stays empty and the
# binary builds offline.

BINARY  := orion
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# RELVER is VERSION with any leading v stripped. Archive names always carry
# exactly one v (orion_v<RELVER>_<os>_<arch>), matching the whodunit tap and
# bucket. Deriving it rather than reusing VERSION keeps the name identical
# whether or not a tag exists: git describe yields "v0.1.0" on a tag but a
# bare sha otherwise, and the templates hardcode the v.
RELVER := $(patsubst v%,%,$(VERSION))
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
# windows/arm64 included: the scoop manifest declares an arm64 architecture,
# and a manifest promising an archive the release does not contain is a
# broken install for anyone on an ARM Windows machine.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

# dist produces the archives brew and scoop consume, in the same layout the
# whodunit tap and bucket already use:
#
#   orion_v<version>_<os>_<arch>.tar.gz   unix
#   orion_v<version>_<os>_<arch>.zip      windows
#   checksums.txt                         name checked by scoop's autoupdate
#
# The archive holds a PLAIN `orion`, not a versioned filename. Homebrew's
# install block does `bin.install "orion"`, so a versioned name inside the
# archive fails with "no such file" on every upgrade.
#
# LICENSE and NOTICE ride along in every archive. Apache-2.0 4(a) requires
# giving recipients a copy of the License, and 4(d) requires the NOTICE to
# travel with derivative works. Shipping a bare binary would distribute the
# software without the terms it is licensed under.
dist:
	@rm -rf dist && mkdir -p dist/stage
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=""; \
	  [ "$$os" = "windows" ] && ext=".exe"; \
	  echo "building $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
	    -ldflags "$(LDFLAGS)" -o dist/stage/$(BINARY)$$ext ./cmd/orion || exit 1; \
	  base=$(BINARY)_v$(RELVER)_$${os}_$${arch}; \
	  cp LICENSE NOTICE dist/stage/ 2>/dev/null || true; \
	  if [ "$$os" = "windows" ]; then \
	    (cd dist/stage && zip -q ../$$base.zip $(BINARY)$$ext LICENSE NOTICE); \
	  else \
	    (cd dist/stage && tar czf ../$$base.tar.gz $(BINARY)$$ext LICENSE NOTICE); \
	  fi; \
	  rm -f dist/stage/$(BINARY)$$ext; \
	done
	@rm -f dist/stage/LICENSE dist/stage/NOTICE
	@rmdir dist/stage
	@cd dist && (shasum -a 256 * 2>/dev/null || sha256sum *) > checksums.txt
	@echo "--- dist/" && ls -1 dist

# packaging renders the brew formula and scoop manifest from the checksums
# this build produced. Kept a separate target so `make dist` stays usable
# without a licence set.
packaging: dist
	@scripts/render-packaging.sh $(RELVER)

clean:
	rm -rf bin dist
