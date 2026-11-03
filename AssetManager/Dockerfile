# syntax=docker/dockerfile:1.7
#
# AssetManager — owns assets, vulnerabilities, detections.
# Pattern: see AIManager/Dockerfile for the rationale.

ARG GO_VERSION=1.24-bookworm
FROM golang:${GO_VERSION} AS builder
WORKDIR /src

# Build context is the repository root (see deployment/docker/compose.yaml) so the
# shared TachyonikLib module (local `replace tachyonik/lib => ../TachyonikLib`) is available.
COPY TachyonikLib/ ./TachyonikLib/

# Download deps before copying the rest of the source, for layer caching.
COPY AssetManager/go.mod AssetManager/go.sum ./AssetManager/
WORKDIR /src/AssetManager
RUN go mod download

WORKDIR /src
COPY AssetManager/ ./AssetManager/
WORKDIR /src/AssetManager
ENV CGO_ENABLED=1
# The build stage has no git — and no .git in the context — so the version
# comes in as an argument. Left unset the binary honestly reports 0.0.0-dev
# rather than claiming a release it cannot verify.
ARG VERSION=0.0.0-dev
RUN go build -trimpath \
      -ldflags="-s -w -X tachyonik/assetmanager/internal/version.Version=$VERSION" \
      -o /out/assetmanager ./cmd/server

FROM gcr.io/distroless/base-debian12:debug AS runtime
COPY --from=builder /out/assetmanager /usr/local/bin/assetmanager
ENV ASSETMANAGER_CONFIG=/etc/tachyonikcsm/assetmanager.yaml
ENV ASSETMANAGER_DB_PATH=/var/lib/tachyonikcsm/AssetManager.db
EXPOSE 8081
HEALTHCHECK --interval=5s --timeout=5s --retries=6 --start-period=20s \
    CMD ["wget", "--spider", "--quiet", "http://127.0.0.1:8081/ready"]
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/assetmanager"]
