# ActionExecutor
# SPDX-FileCopyrightText: 2026 Tachyonik GmbH
# SPDX-License-Identifier: AGPL-3.0-or-later

# syntax=docker/dockerfile:1.7
#
# ActionExecutor — event-driven daemon that runs execution-rule routines,
# automatically for new actions and on users' requests handed over by
# AIManager. No HTTP server (CLAUDE.md rule). Liveness is observed via
# SystemManager's service_heartbeats table, not via a port probe — Compose
# just restarts the container if the process exits.
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
COPY ActionExecutor/go.mod ActionExecutor/go.sum ./ActionExecutor/
WORKDIR /src/ActionExecutor
RUN go mod download

# Service source.
WORKDIR /src
COPY ActionExecutor/ ./ActionExecutor/
WORKDIR /src/ActionExecutor
ENV CGO_ENABLED=1
# The build stage has no git — and no .git in the context — so the version
# comes in as an argument. Left unset the binary honestly reports 0.0.0-dev
# rather than claiming a release it cannot verify.
ARG VERSION=0.0.0-dev
RUN go build -trimpath \
      -ldflags="-s -w -X tachyonik/actionexecutor/internal/version.Version=$VERSION" \
      -o /out/actionexecutor ./cmd/daemon

FROM gcr.io/distroless/base-debian12:debug AS runtime
COPY --from=builder /out/actionexecutor /usr/local/bin/actionexecutor
ENV ACTIONEXECUTOR_CONFIG=/etc/tachyonikcsm/actionexecutor.yaml
# No EXPOSE / HEALTHCHECK — see header note.
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/actionexecutor"]
