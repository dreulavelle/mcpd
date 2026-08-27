BINARY  := mcpd
# Stripped of its leading v so a local build names itself the way a release
# does. Empty only outside a git checkout, where the binary falls back to what
# the build itself records.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -s -w -X github.com/spoked/mcpd/internal/app.Version=$(VERSION)

# The pure-Go SQLite driver is what makes a static binary possible; building
# with CGO would silently give that up.
export CGO_ENABLED = 0

.PHONY: all
all: check build

.PHONY: build
build: web
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) ./cmd/mcpd

# The dashboard is embedded in the binary, so its bundle has to exist before
# the Go build runs. The copy step keeps the embed directive pointing at a
# path inside the package, which go:embed requires.
.PHONY: web
web:
	cd web && npm ci --silent && npm run build
	rm -rf internal/admin/dist
	cp -r web/dist internal/admin/dist

.PHONY: test
test:
	go test ./...

# The dashboard's own tests. Separate from `test` because they need node
# rather than go, and separate from `web` because building the bundle should
# not depend on a toolchain a Go-only change never touches.
.PHONY: web-test
web-test:
	cd web && npm ci --silent && npm test

# The race detector needs cgo, so it overrides the package-level setting.
.PHONY: race
race:
	CGO_ENABLED=1 go test -race -count=1 ./...

.PHONY: check
check: fmt vet test verify-deps

.PHONY: fmt
# The packages go knows about rather than the whole tree, so ./data -- which
# holds a deployment's database and TLS material -- is never walked.
fmt:
	@gofmt -l $$(go list -f '{{.Dir}}' ./...) | grep -v '^$$' && { echo "gofmt needed on the files above"; exit 1; } || true

.PHONY: vet
vet:
	go vet ./...

# modernc.org/sqlite requires modernc.org/libc at the exact version in its own
# go.mod; a mismatch fails at runtime rather than at build time, so it is
# checked here instead of being left to chance.
.PHONY: verify-deps
verify-deps:
	@sqlite_ver=$$(go list -m -f '{{.Version}}' modernc.org/sqlite); \
	want=$$(go list -m -f '{{.Dir}}' modernc.org/sqlite | xargs -I{} sh -c 'grep -E "^\s*modernc\.org/libc v" {}/go.mod | head -1 | awk "{print \$$2}"'); \
	have=$$(go list -m -f '{{.Version}}' modernc.org/libc); \
	if [ "$$want" != "$$have" ]; then \
		echo "modernc.org/libc is $$have but sqlite $$sqlite_ver expects $$want"; \
		echo "run: go get modernc.org/libc@$$want"; \
		exit 1; \
	fi; \
	echo "modernc.org/libc $$have matches sqlite $$sqlite_ver"

.PHONY: run
run: build
	./bin/$(BINARY) -config configs/example.yaml

.PHONY: docker
docker:
	docker build --build-arg VERSION=$(VERSION) -t ghcr.io/dreulavelle/mcpd:$(VERSION) .

.PHONY: clean
clean:
	rm -rf bin dist web/dist web/node_modules
