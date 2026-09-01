# syntax=docker/dockerfile:1

# ---- dashboard --------------------------------------------------------------
#
# Pinned to the build platform: the bundle is the same bytes on any target, so
# building it once natively avoids emulating this per architecture.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web

WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install --silent

COPY web/ ./
RUN npm run build

# ---- build ------------------------------------------------------------------
#
# Cross-compiled from the build platform. Running this under QEMU per target
# would emulate a Go compiler, which is the slowest part of any multi-arch
# build -- and Go cross-compiles natively with CGO off.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies first so the module cache survives source edits.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# The dashboard bundle must sit inside the package for go:embed to reach it.
COPY --from=web /web/dist ./internal/admin/dist

# Empty rather than "dev". A release build is told its version by CI, which
# takes it from the tag; anything else derives one below from the release
# manifest that is committed to the source tree. Nothing reads it from the
# environment, because a version typed into a file beside the deployment is a
# second answer to a question the source already answers, and it is the one
# that goes stale.
ARG VERSION=
# Supplied by buildx per target; defaulted for a plain `docker build`.
ARG TARGETARCH
ARG TARGETOS=linux
# CGO_ENABLED=0 is not an optimisation here, it is the point: the SQLite driver
# is pure Go, so the result is a static binary with no libc to keep patched.
# Alpine below runs it as-is; nothing links against musl.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    version="${VERSION}"; \
    if [ -z "$version" ]; then \
      # release-please writes the last released version here on every release,
      # so it is the nearest thing in the tree to the truth.
      #
      # Reported bare, as x.y.z. It used to carry a +source suffix marking a
      # build that is that release plus whatever else is in the working copy;
      # the version is read by people far more often than by anything ordering
      # releases, and a suffix in front of every one of them was not paying for
      # itself. The cost is accepted deliberately: a build from a working tree
      # now reports the same string as the release it was cut after.
      base="$(sed -n 's/.*"\."[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
              .release-please-manifest.json)"; \
      version="${base:-0.0.0}"; \
    fi; \
    echo "building mcpd $version"; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath \
      -ldflags="-s -w -X github.com/spoked/mcpd/internal/app.Version=${version}" \
      -o /out/mcpd ./cmd/mcpd

# ---- artifacts --------------------------------------------------------------
#
# Export only: `--target artifacts --output type=local` writes out the same
# binary the runtime stage below copies in.
FROM scratch AS artifacts
COPY --from=build /out/mcpd /mcpd

# ---- runtime --------------------------------------------------------------
FROM alpine:3.22

# Alpine rather than distroless, and the trade is worth stating plainly.
#
# What is given up: a shell in the image gives a remote code execution more to
# work with, and there is a package manager and a musl userland to keep
# patched.
#
# What is not given up, because it is what does the hardening: the root
# filesystem is read-only, every capability is dropped, no-new-privileges is
# set, the process is nonroot, and the binary is static and CGO-free.
#
# What is bought: the two things distroless made impossible, and they were the
# whole of the complaint. The container can generate its own configuration on
# first start, so `docker compose up` on an empty directory produces a working
# host; and it can run as the host user's uid, so the data directory is one an
# operator can read and edit without sudo. Under distroless the volume had to
# be pre-chowned to uid 65532, which is why the data directory was hidden
# behind a leading dot -- `go build ./...` could not read it otherwise.
#
# For a self-hosted control plane on a LAN, that is the right side of the
# trade.
RUN apk add --no-cache ca-certificates

COPY --from=build /out/mcpd /usr/local/bin/mcpd
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
# A reference config, not the one that runs. Everything a deployment uses is
# generated into the data directory on first start.
COPY configs/example.yaml /usr/share/doc/mcpd/example.yaml

# uid 1000 is the first login account on every mainstream distribution, so the
# image's default already matches the common case. compose overrides it with
# the host user's own; this is what `docker run` gets.
RUN addgroup -g 1000 mcpd \
 && adduser -u 1000 -G mcpd -h /var/lib/mcpd -D mcpd \
 && chmod 0755 /usr/local/bin/docker-entrypoint.sh \
 && install -d -o mcpd -g mcpd -m 0750 /var/lib/mcpd

USER mcpd:mcpd

# One directory: config.yaml, the database, TLS material, logs and plugins. A
# named volume inherits ownership from the image's directory on first use,
# which is what makes `docker run` with no bind mount work.
VOLUME ["/var/lib/mcpd"]

# 8080 is the MCP endpoint; 8081 is the admin dashboard. Both are
# unprivileged so the container needs no capabilities -- the host publishes
# port 80 for the dashboard by mapping to 8081.
EXPOSE 8080 8081

# Exec form by necessity as well as preference: the entrypoint execs mcpd, so
# it receives SIGTERM directly as PID 1 and runs its own graceful drain.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
