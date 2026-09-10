# SourceAnalyser
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later

# Resolved from this module's own namespaced git tag `sourceanalyser/X.Y.Z`, so
# the analyser's version is independent of TachyonikCSM's (which lives in
# WebUI/package.json) and of every other module in the monorepo: `--match`
# ignores all other tags, and the prefix is stripped to a numeric x.y.z. On the
# release tag `sourceanalyser/1.1.1` → `1.1.1`. Off-tag or dirty builds get a
# descriptive suffix; a tree with no matching tag falls back to 0.0.0-dev.
# Override with `make … VERSION=1.1.1` for a deterministic build (CI, Docker).
VERSION ?= $(patsubst sourceanalyser/%,%,$(shell git describe --tags --match 'sourceanalyser/*' --dirty 2>/dev/null || echo 0.0.0-dev))
MODULE  := tachyonik/sourceanalyser
BINARY  := tachyonik-sourceanalyser

# The version is the only thing injected. Unlike TachyonikProxy this module
# ships as a container image rather than as .deb/.rpm/.msi with a self-update
# manifest, so there is no packaging apparatus to attach a release guard to.
LDFLAGS := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)

.PHONY: build test vet clean version

build: ## Build the daemon with the version embedded.
	CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/daemon

test: ## Run the test suite.
	go test ./...

vet: ## Run go vet.
	go vet ./...

version: ## Print the version this tree would build as.
	@echo $(VERSION)

clean: ## Remove the built binary.
	rm -f $(BINARY)
