# SourceAnalyser
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later

# syntax=docker/dockerfile:1.7
#
# SourceAnalyser — event-driven daemon. No HTTP server.
# Liveness via heartbeats; Compose restarts the container if it exits.
# Pattern: see AIManager/Dockerfile for the rationale.

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
COPY SourceAnalyser/go.mod SourceAnalyser/go.sum ./SourceAnalyser/
WORKDIR /src/SourceAnalyser
RUN go mod download

# Service source.
WORKDIR /src
COPY SourceAnalyser/ ./SourceAnalyser/
WORKDIR /src/SourceAnalyser
ENV CGO_ENABLED=1
RUN go build -trimpath -ldflags="-s -w" -o /out/sourceanalyser ./cmd/daemon

FROM gcr.io/distroless/base-debian12:debug AS runtime
COPY --from=builder /out/sourceanalyser /usr/local/bin/sourceanalyser
ENV SOURCEANALYSER_CONFIG=/etc/tachyonikcsm/sourceanalyser.yaml
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sourceanalyser"]
