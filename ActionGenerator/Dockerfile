# ActionGenerator
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later

# syntax=docker/dockerfile:1.7
#
# ActionGenerator — event-driven daemon. No HTTP server (CLAUDE.md
# rule). Liveness is observed via SystemManager's service_heartbeats
# table, not via a port probe — Compose just restarts the container if
# the process exits.
# Pattern: see AIManager/Dockerfile for the rationale (CGO, distroless,
# nonroot user).

# NOTE: the build context is the repository root (see deployment/docker/compose.yaml)
# so the shared TachyonikLib module, referenced via a local `replace`, is available.
ARG GO_VERSION=1.24-bookworm
FROM golang:${GO_VERSION} AS builder
WORKDIR /src

# Shared library (local module referenced via `replace tachyonik/lib => ../TachyonikLib`).
# Copied first: it changes rarely and the dependency download doesn't need its source.
COPY TachyonikLib/ ./TachyonikLib/

# Download remote dependencies before copying the rest of the source, so the
# module-download layer stays cached across source-only changes.
COPY ActionGenerator/go.mod ActionGenerator/go.sum ./ActionGenerator/
WORKDIR /src/ActionGenerator
RUN go mod download

# Service source.
WORKDIR /src
COPY ActionGenerator/ ./ActionGenerator/
WORKDIR /src/ActionGenerator
ENV CGO_ENABLED=1
RUN go build -trimpath -ldflags="-s -w" -o /out/actiongenerator ./cmd/daemon

FROM gcr.io/distroless/base-debian12:debug AS runtime
COPY --from=builder /out/actiongenerator /usr/local/bin/actiongenerator
ENV ACTIONGENERATOR_CONFIG=/etc/tachyonikcsm/actiongenerator.yaml
# No EXPOSE / HEALTHCHECK — see header note.
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/actiongenerator"]
