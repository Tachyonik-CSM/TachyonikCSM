# TachyonikProxy
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later

# Resolved from this module's own namespaced git tag `tachyonikproxy/X.Y.Z`,
# so the proxy's version is independent of TachyonikCSM and every other module
# in the monorepo: `--match` ignores all other tags, and the prefix is stripped
# to a numeric x.y.z. On the release tag `tachyonikproxy/1.0.0` → `1.0.0`.
# Off-tag/dirty builds get a descriptive suffix; a tree with no matching tag
# falls back to 0.0.0-dev. None of those non-numeric forms may be packaged —
# see the _require-release-version guard below. Override with
# `make … VERSION=1.0.0` for a deterministic build (CI).
VERSION  ?= $(patsubst tachyonikproxy/%,%,$(shell git describe --tags --match 'tachyonikproxy/*' --dirty 2>/dev/null || echo 0.0.0-dev))
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

# Base URL where release artifacts are hosted — used to build the auto-update
# manifest's artifact URLs (the `manifest` target). Defaults to the canonical
# download path; override for staging: `make manifest BASE_URL=https://staging/…`.
BASE_URL ?= https://tachyonik.com/download/proxy/

.PHONY: build build-all clean test _require-release-version \
        package-deb package-rpm package-macos package-windows \
        package-archives package-all manifest release check-packaging

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

# Packaging checks. Separate from `test` because they shell out to docker to
# drive real dpkg and rpm transactions — the ordering bugs they catch (an
# upgrade leaving the service stopped, or disabled) are invisible to any check
# that reads the maintainer scripts one at a time.
check-packaging:
	scripts/check-latest-urls.sh
	scripts/check-package-lifecycle.sh

# ─── Release-version guard ──────────────────────────────────────────
# package-* refuses to run unless VERSION is a clean numeric x.y.z, so a
# dirty tree, an off-tag build (1.0.0-3-gabc123), a bare commit hash, or the
# no-git 0.0.0-dev fallback can never produce a publishable, mislabeled
# artifact (the proxy's self-update also requires a purely numeric version).
# Tag the release commit (`git tag 1.0.0`) or pass VERSION=1.0.0.
_require-release-version:
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { \
		echo "ERROR: refusing to package non-release version '$(VERSION)'."; \
		echo "       Need a clean numeric x.y.z. Build the tagged release commit"; \
		echo "       (e.g. git tag 1.0.0) or pass VERSION=1.0.0 explicitly."; \
		exit 1; }

# ─── Linux packages (.deb + .rpm) via nfpm ──────────────────────────

package-deb: _require-release-version build-linux-amd64 build-linux-arm64
	@command -v nfpm >/dev/null 2>&1 || { echo "nfpm not found. Install: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.1"; exit 1; }
	TACHYONIKPROXY_VERSION=$(VERSION) TACHYONIKPROXY_ARCH=amd64 nfpm package -p deb -f nfpm.yaml -t $(DIST)/
	TACHYONIKPROXY_VERSION=$(VERSION) TACHYONIKPROXY_ARCH=arm64 nfpm package -p deb -f nfpm.yaml -t $(DIST)/

package-rpm: _require-release-version build-linux-amd64 build-linux-arm64
	@command -v nfpm >/dev/null 2>&1 || { echo "nfpm not found. Install: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.1"; exit 1; }
	TACHYONIKPROXY_VERSION=$(VERSION) TACHYONIKPROXY_ARCH=amd64 nfpm package -p rpm -f nfpm.yaml -t $(DIST)/
	TACHYONIKPROXY_VERSION=$(VERSION) TACHYONIKPROXY_ARCH=arm64 nfpm package -p rpm -f nfpm.yaml -t $(DIST)/

# ─── macOS installer (.pkg) ─────────────────────────────────────────

package-macos: _require-release-version build-darwin-amd64 build-darwin-arm64
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

package-windows: _require-release-version build-windows-amd64
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

package-archives: _require-release-version build-all
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

# ─── Auto-update manifest ───────────────────────────────────────────
# Hash the four self-update archives and write dist/manifest.json. Depends on
# package-archives (so the tarballs exist and the release-version guard has
# run). Signing stays a separate, OFFLINE step — see scripts/sign-manifest.sh
# and RELEASING.md.

manifest: package-archives
	scripts/make-manifest.sh -v $(VERSION) -u $(BASE_URL) -d $(DIST)

# ─── Full release build (packages + manifest, unsigned) ─────────────
# One command for everything publishable from the build host. The manifest
# still needs to be signed offline and uploaded with the artifacts.

release: package-all manifest
	@echo ""
	@echo "Release $(VERSION) staged in $(DIST)/ (packages + manifest.json)."
	@echo "Next: sign dist/manifest.json offline (scripts/sign-manifest.sh), then upload."

# ─── Clean ──────────────────────────────────────────────────────────

clean:
	rm -rf $(DIST)
