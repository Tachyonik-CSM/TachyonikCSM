# ActionGenerator
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later

# Resolved from this module's own namespaced git tag `actiongenerator/X.Y.Z`, so
# the daemon's version is independent of TachyonikCSM's (which lives in
# WebUI/package.json) and of every other module in the monorepo: `--match`
# ignores all other tags, and the prefix is stripped to a numeric x.y.z. On the
# release tag `actiongenerator/1.0.0` → `1.0.0`. Off-tag or dirty builds get a
# descriptive suffix; a tree with no matching tag falls back to 0.0.0-dev.
# Override with `make … VERSION=1.0.0` for a deterministic build (CI, Docker).
VERSION ?= $(patsubst actiongenerator/%,%,$(shell git describe --tags --match 'actiongenerator/*' --dirty 2>/dev/null || echo 0.0.0-dev))
MODULE  := tachyonik/actiongenerator
BINARY  := tachyonik-actiongenerator

# The version is the only thing injected, and it identifies the build rather
# than steering it: nothing compares this value or stamps it into a record. See
# internal/version for why that is deliberate.
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
