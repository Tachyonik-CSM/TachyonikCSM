# SourceImporter
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later

# syntax=docker/dockerfile:1.7
#
# SourceImporter — event-driven daemon. No HTTP server (CLAUDE.md rule).
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
COPY SourceImporter/go.mod SourceImporter/go.sum ./SourceImporter/
WORKDIR /src/SourceImporter
RUN go mod download

# Service source.
WORKDIR /src
COPY SourceImporter/ ./SourceImporter/
WORKDIR /src/SourceImporter
ENV CGO_ENABLED=1
# The build stage has no git — and no .git in the context — so the version
# comes in as an argument. Left unset the binary honestly reports 0.0.0-dev
# rather than claiming a release it cannot verify.
ARG VERSION=0.0.0-dev
RUN go build -trimpath \
      -ldflags="-s -w -X tachyonik/sourceimporter/internal/version.Version=$VERSION" \
      -o /out/sourceimporter ./cmd/daemon

FROM gcr.io/distroless/base-debian12:debug AS runtime
COPY --from=builder /out/sourceimporter /usr/local/bin/sourceimporter
ENV SOURCEIMPORTER_CONFIG=/etc/tachyonikcsm/sourceimporter.yaml
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sourceimporter"]
