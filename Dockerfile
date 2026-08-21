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

# The state directory has to exist in the image, owned by the user that will
# run. A named volume inherits its ownership from the image's directory on
# first use, so without this the volume is root-owned and a nonroot process
# cannot open the database. Distroless has no shell, so it cannot be fixed at
# runtime with an entrypoint chown.
RUN mkdir -p /out/state

ARG VERSION=dev
# CGO_ENABLED=0 is not an optimisation here, it is the point: the SQLite driver
# is pure Go, so the result is a static binary that runs on a distroless static
# base with no libc and nothing to keep patched.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X github.com/spoked/mcpd/internal/app.Version=${VERSION}" \
      -o /out/mcpd ./cmd/mcpd

# ---- runtime --------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

# Distroless static carries no shell and no package manager, so there is
# nothing in the image to exploit beyond mcpd itself.
COPY --from=build /out/mcpd /usr/local/bin/mcpd
COPY --from=build --chown=65532:65532 /out/state /var/lib/mcpd
COPY --from=build /src/configs/example.yaml /etc/mcpd/config.yaml

# 65532 is distroless's nonroot user; the data volume must be writable by it.
USER nonroot:nonroot

VOLUME ["/var/lib/mcpd"]

# 8080 is the MCP endpoint; 8081 is the admin dashboard. Both are
# unprivileged so the container needs no capabilities -- the host publishes
# port 80 for the dashboard by mapping to 8081.
EXPOSE 8080 8081

# Exec form by necessity as well as preference: mcpd receives SIGTERM directly
# as PID 1 and runs its own graceful drain.
ENTRYPOINT ["/usr/local/bin/mcpd"]
CMD ["-config", "/etc/mcpd/config.yaml"]
