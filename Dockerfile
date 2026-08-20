# syntax=docker/dockerfile:1

# ---- build ----------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first so the module cache survives source edits.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

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
COPY --from=build /src/configs/example.yaml /etc/mcpd/config.yaml

# 65532 is distroless's nonroot user; the data volume must be writable by it.
USER nonroot:nonroot

VOLUME ["/var/lib/mcpd"]
EXPOSE 8080

# Exec form by necessity as well as preference: mcpd receives SIGTERM directly
# as PID 1 and runs its own graceful drain.
ENTRYPOINT ["/usr/local/bin/mcpd"]
CMD ["-config", "/etc/mcpd/config.yaml"]
