# meandr-cli — build, test, install.
#
# Needs nothing but a Go toolchain.

BINARY      := meandr
MODULE      := github.com/meandr-inc/meandr-cli
PREFIX      ?= /usr/local
BINDIR      ?= $(PREFIX)/bin

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# The service address this binary connects to, built in so the tunnel
# command needs no endpoint flag. Override to build against another:
#
#	make build ENDPOINT=tun.example.com:8443
#
# Makefile.local is untracked and included below, for local overrides.
ENDPOINT    ?= tun.meandr.io

-include Makefile.local

# -s -w drop the symbol table and DWARF, for a third off the binary.
LDFLAGS := -s -w \
	-X '$(MODULE)/internal/version.version=$(VERSION)' \
	-X '$(MODULE)/internal/version.commit=$(COMMIT)' \
	-X '$(MODULE)/internal/version.date=$(DATE)' \
	-X 'main.defaultEndpoint=$(ENDPOINT)'

# No VCS stamping, since the version is passed above. No cgo, so the
# release binary runs on any glibc or musl host.
GOFLAGS := -trimpath -buildvcs=false
export CGO_ENABLED := 0

.PHONY: all build install uninstall test vet fmt clean release

all: build

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/meandr

install: build
	install -d $(DESTDIR)$(BINDIR)
	install -m 0755 $(BINARY) $(DESTDIR)$(BINDIR)/$(BINARY)

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/$(BINARY)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -f $(BINARY)
	rm -rf dist

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# LICENSE and NOTICE ship with the binaries: the third-party licenses
# require their notices to travel with a binary distribution.
release:
	@mkdir -p dist
	@rm -f dist/SHA256SUMS
	@cp LICENSE NOTICE dist/
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  out="dist/$(BINARY)-$${os}-$${arch}"; \
	  echo "  $$out"; \
	  GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o "$$out" ./cmd/meandr || exit 1; \
	done
	@cd dist && shasum -a 256 $(BINARY)-* > SHA256SUMS
