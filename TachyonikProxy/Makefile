# TachyonikProxy
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.6.0")
MODULE   := tachyonik/tachyonikproxy
BINARY   := tachyonikproxy
LDFLAGS  := -s -w -X main.version=$(VERSION)
DIST     := dist
# Unversioned build intermediates (the version is embedded via ldflags)
# live under dist/bin/ so dist/ itself holds only the versioned,
# publishable artifacts (.deb/.rpm/.pkg/.msi/.tar.gz/.zip).
BINDIR   := $(DIST)/bin

# Tools installed via `go install` (nfpm, go-msi) land in GOPATH/bin,
# which is not on everyone's PATH — make recipes find them regardless.
export PATH := $(PATH):$(shell go env GOPATH)/bin

# Build targets: os/arch pairs
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: build build-all clean test \
        package-deb package-rpm package-macos package-windows \
        package-archives package-all

# ─── Single-platform build (native) ────────────────────────────────

build:
	@mkdir -p $(BINDIR)
	go build -ldflags '$(LDFLAGS)' -o $(BINDIR)/$(BINARY) ./cmd/server

# ─── Cross-compile all platforms ────────────────────────────────────

build-all: $(addprefix build-,$(subst /,-,$(PLATFORMS)))

build-linux-amd64:
	@mkdir -p $(BINDIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(BINDIR)/$(BINARY)-linux-amd64 ./cmd/server

build-linux-arm64:
	@mkdir -p $(BINDIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(BINDIR)/$(BINARY)-linux-arm64 ./cmd/server

build-darwin-amd64:
	@mkdir -p $(BINDIR)
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(BINDIR)/$(BINARY)-darwin-amd64 ./cmd/server

build-darwin-arm64:
	@mkdir -p $(BINDIR)
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(BINDIR)/$(BINARY)-darwin-arm64 ./cmd/server

build-windows-amd64:
	@mkdir -p $(BINDIR)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(BINDIR)/$(BINARY)-windows-amd64.exe ./cmd/server

# ─── Tests ──────────────────────────────────────────────────────────

test:
	go test ./...

# ─── Linux packages (.deb + .rpm) via nfpm ──────────────────────────

package-deb: build-linux-amd64 build-linux-arm64
	@command -v nfpm >/dev/null 2>&1 || { echo "nfpm not found. Install: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.1"; exit 1; }
	TACHYONIKPROXY_VERSION=$(VERSION) TACHYONIKPROXY_ARCH=amd64 nfpm package -p deb -f nfpm.yaml -t $(DIST)/
	TACHYONIKPROXY_VERSION=$(VERSION) TACHYONIKPROXY_ARCH=arm64 nfpm package -p deb -f nfpm.yaml -t $(DIST)/

package-rpm: build-linux-amd64 build-linux-arm64
	@command -v nfpm >/dev/null 2>&1 || { echo "nfpm not found. Install: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.1"; exit 1; }
	TACHYONIKPROXY_VERSION=$(VERSION) TACHYONIKPROXY_ARCH=amd64 nfpm package -p rpm -f nfpm.yaml -t $(DIST)/
	TACHYONIKPROXY_VERSION=$(VERSION) TACHYONIKPROXY_ARCH=arm64 nfpm package -p rpm -f nfpm.yaml -t $(DIST)/

# ─── macOS installer (.pkg) ─────────────────────────────────────────

package-macos: build-darwin-amd64 build-darwin-arm64
ifeq (,$(shell command -v pkgbuild 2>/dev/null))
	@echo "pkgbuild not available (requires a macOS host) — skipping .pkg (use package-archives for .tar.gz)"
else
	@# amd64
	$(call build_pkg,darwin-amd64,amd64)
	@# arm64
	$(call build_pkg,darwin-arm64,arm64)
endif

define build_pkg
	$(eval STAGE := $(DIST)/pkg-stage-$(2))
	rm -rf $(STAGE)
	mkdir -p $(STAGE)/usr/local/bin
	mkdir -p $(STAGE)/etc/tachyonik/tachyonikproxy
	mkdir -p $(STAGE)/Library/LaunchDaemons
	cp $(BINDIR)/$(BINARY)-$(1) $(STAGE)/usr/local/bin/$(BINARY)
	chmod 755 $(STAGE)/usr/local/bin/$(BINARY)
	cp packaging/config.yaml.default $(STAGE)/etc/tachyonik/tachyonikproxy/config.yaml.default
	cp packaging/launchd/com.tachyonik.tachyonikproxy.plist $(STAGE)/Library/LaunchDaemons/
	pkgbuild \
		--root $(STAGE) \
		--identifier com.tachyonik.tachyonikproxy \
		--version $(VERSION) \
		--scripts packaging/scripts \
		$(DIST)/tachyonikproxy-$(VERSION)-darwin-$(2).pkg
	rm -rf $(STAGE)
endef

# ─── Windows installer (.msi) ───────────────────────────────────────
# Requires: go-msi (go install github.com/mh-cbon/go-msi@latest) + WiX on Windows
# Falls back to .zip-only on non-Windows build hosts.

package-windows: build-windows-amd64
	@if command -v go-msi >/dev/null 2>&1; then \
		mkdir -p $(DIST)/msi-stage; \
		cp $(BINDIR)/$(BINARY)-windows-amd64.exe $(DIST)/msi-stage/$(BINARY).exe; \
		cp packaging/config.yaml.default $(DIST)/msi-stage/config.yaml; \
		go-msi make \
			--src packaging/windows/wix.json \
			--msi $(DIST)/tachyonikproxy-$(VERSION)-windows-amd64.msi \
			--version $(VERSION) \
			--path $(DIST)/msi-stage; \
		rm -rf $(DIST)/msi-stage; \
	else \
		echo "go-msi/WiX not available — skipping .msi (use package-archives for .zip)"; \
	fi

# ─── Portable archives (.tar.gz / .zip) ─────────────────────────────

package-archives: build-all
	@# Linux tar.gz
	$(call build_tar,linux-amd64)
	$(call build_tar,linux-arm64)
	@# macOS tar.gz
	$(call build_tar,darwin-amd64)
	$(call build_tar,darwin-arm64)
	@# Windows zip
	$(call build_zip,windows-amd64)

define build_tar
	$(eval STAGE := $(DIST)/archive-$(1))
	rm -rf $(STAGE)
	mkdir -p $(STAGE)/tachyonikproxy-$(VERSION)
	cp $(BINDIR)/$(BINARY)-$(1) $(STAGE)/tachyonikproxy-$(VERSION)/$(BINARY)
	chmod 755 $(STAGE)/tachyonikproxy-$(VERSION)/$(BINARY)
	cp packaging/config.yaml.default $(STAGE)/tachyonikproxy-$(VERSION)/config.yaml
	cp packaging/systemd/tachyonikproxy.service $(STAGE)/tachyonikproxy-$(VERSION)/ 2>/dev/null || true
	tar -czf $(DIST)/tachyonikproxy-$(VERSION)-$(1).tar.gz -C $(STAGE) tachyonikproxy-$(VERSION)
	rm -rf $(STAGE)
endef

define build_zip
	$(eval STAGE := $(DIST)/archive-$(1))
	rm -rf $(STAGE)
	mkdir -p $(STAGE)/tachyonikproxy-$(VERSION)
	cp $(BINDIR)/$(BINARY)-$(1).exe $(STAGE)/tachyonikproxy-$(VERSION)/$(BINARY).exe
	cp packaging/config.yaml.default $(STAGE)/tachyonikproxy-$(VERSION)/config.yaml
	cd $(STAGE) && zip -r ../tachyonikproxy-$(VERSION)-$(1).zip tachyonikproxy-$(VERSION)
	rm -rf $(STAGE)
endef

# ─── All packages ───────────────────────────────────────────────────

package-all: package-deb package-rpm package-macos package-windows package-archives
	@echo ""
	@echo "All packages built in $(DIST)/:"
	@ls -1 $(DIST)/*.deb $(DIST)/*.rpm $(DIST)/*.pkg $(DIST)/*.msi $(DIST)/*.tar.gz $(DIST)/*.zip 2>/dev/null || true

# ─── Clean ──────────────────────────────────────────────────────────

clean:
	rm -rf $(DIST)
