# syntax=docker/dockerfile:1

# ---- dashboard --------------------------------------------------------------
FROM node:24-alpine AS web

WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install --silent

COPY web/ ./
RUN npm run build

# ---- build ------------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first so the module cache survives source edits.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# The dashboard bundle must sit inside the package for go:embed to reach it.
COPY --from=web /web/dist ./internal/admin/dist

ARG VERSION=dev
# CGO_ENABLED=0 is not an optimisation here, it is the point: the SQLite driver
# is pure Go, so the result is a static binary with no libc to keep patched.
# Alpine below runs it as-is; nothing links against musl.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X github.com/spoked/mcpd/internal/app.Version=${VERSION}" \
      -o /out/mcpd ./cmd/mcpd

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

# One directory: config.yaml, the database, TLS material and plugins. A named
# volume inherits ownership from the image's directory on first use, which is
# what makes `docker run` with no bind mount work.
VOLUME ["/var/lib/mcpd"]

# 8080 is the MCP endpoint; 8081 is the admin dashboard. Both are
# unprivileged so the container needs no capabilities -- the host publishes
# port 80 for the dashboard by mapping to 8081.
EXPOSE 8080 8081

# Exec form by necessity as well as preference: the entrypoint execs mcpd, so
# it receives SIGTERM directly as PID 1 and runs its own graceful drain.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
