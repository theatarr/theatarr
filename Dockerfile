# syntax=docker/dockerfile:1
# Theatarr: one static binary (CGO_ENABLED=0, modernc SQLite), run as non-root with its database on the /data volume.
#   docker build --build-arg VERSION=$(git describe --tags --always --dirty) -t theatarr:dev .
#   docker run -v theatarr-data:/data -v /path/to/policy:/config/policy:ro -p 7700:7700 theatarr:dev

# The web UI: npm ci and vite build in web/, output in internal/webui/dist (embedded by the Go build below).
FROM node:20.20.1-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY internal/web/openapi.json /src/internal/web/openapi.json
COPY web/ ./
RUN mkdir -p /src/internal/webui/dist && npm run build

FROM golang:1.27.3-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY . .
COPY --from=ui /src/internal/webui/dist/ internal/webui/dist/
ARG VERSION=dev
# -buildvcs=false: the build context has no usable .git (worktrees, .dockerignore); VERSION carries the revision.
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -buildvcs=false -ldflags "-s -w -X github.com/theatarr/theatarr/internal/buildinfo.Version=${VERSION}" \
        -o /out/bin/theatarr ./cmd/theatarr \
 && mkdir -p /out/data/backups

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bin/ /usr/local/bin/
COPY --from=build --chown=65532:65532 /out/data /data
# The example policy, for a smoke test: theatarr serve --policy /usr/share/theatarr/examples/policy
COPY examples/policy /usr/share/theatarr/examples/policy
# Licences: Theatarr's own, the third-party notices, and the TRaSH-Guides licence (MIT) for the snapshot the binary embeds.
COPY LICENSE NOTICE internal/trashbundle/LICENSE-TRaSH-Guides /licenses/
ENV THEATARR_DB=/data/theatarr.db \
    THEATARR_POLICY=/config/policy \
    THEATARR_LISTEN=:7700 \
    THEATARR_BACKUP_DIR=/data/backups \
    THEATARR_LOG_LEVEL=info
USER 65532:65532
VOLUME ["/data"]
EXPOSE 7700
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["/usr/local/bin/theatarr", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/theatarr"]
CMD ["serve"]
