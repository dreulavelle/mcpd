# mcpd

An extensible host for Model Context Protocol integrations. One binary serves
many plugins, each on its own endpoint, with every infrastructure mutation
gated behind a durable approval workflow.

```
                         ChatGPT
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
       /mcp/cnmaestro  /mcp/netbox  /mcp/proxmox
              └─────────────┼─────────────┘
                            ▼
                          mcpd
                     (single binary)
                            │
                   ┌────────┴────────┐
                   ▼                 ▼
              plugins            SQLite
```

## Why it is built this way

**SQLite is the only authority.** Whether a change was approved is answered by
a row in a database, never by a message on a bus. Every executor reloads and
revalidates before acting, so a lost, duplicated, or forged event costs
latency at worst.

**Nothing writes without an approval it can prove.** A mutation's payload is
hashed at proposal time and frozen. Before execution the hash is recomputed and
compared inside the same statement that claims the operation, so a tampered
payload cannot execute even if it slipped past every check above it.

**An ambiguous outcome is recorded as ambiguous.** If the process dies between
issuing an upstream write and recording its result, the operation lands in
`indeterminate`, not `failed`. Calling it a failure invites a retry, and the
retry double-applies the change.

**Access is per plugin.** A credential lists the plugins it may reach.
Everything else returns 404 — not 403 — so an agent scoped to one integration
cannot discover which others are deployed.

## Status

Working today:

- Operations domain: state machine, guards, canonical hashing, risk model
- SQLite storage: migrations, six transactions, hash-chained audit, outbox
- Auth: static bearer tokens, roles, per-plugin scoping
- MCP host: `/mcp/{plugin}` routing, stateless streamable HTTP, health,
  OAuth protected-resource metadata
- Plugin contract, registry, and lifecycle; `echo` reference plugin
- Docker image (24 MB, distroless, nonroot) and Compose setup

Next, in order:

1. OAuth resource-server verifier — required for the ChatGPT connector
2. Operations service, executor, verifier, reaper — the runtime half of the
   approval engine
3. cnMaestro plugin: full read surface, then typed mutations
4. Admin dashboard (TS/React)
5. Go plugin SDK for out-of-process plugins in `/plugins`

## Quick start

### Docker

```bash
cp .env.example .env
# generate a token:  openssl rand -base64 32
$EDITOR .env
docker compose up -d
```

To expose it to ChatGPT over HTTPS without opening a port, create a Cloudflare
Tunnel pointing at `http://mcpd:8080`, put its token in `.env`, and run:

```bash
docker compose --profile tunnel up -d
```

### From source

```bash
make build
MCPD_TOKEN_CHATGPT=$(openssl rand -base64 32) \
  ./bin/mcpd -config configs/example.yaml
```

Validate a config without starting anything:

```bash
./bin/mcpd -config /etc/mcpd/config.yaml -check
```

## Verifying it works

```bash
TOKEN=...   # the value of MCPD_TOKEN_CHATGPT

curl -s localhost:8080/health/ready | jq

curl -s -X POST localhost:8080/mcp/echo \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
```

A token scoped to `echo` gets 404 from every other endpoint:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/mcp/proxmox \
  -H "Authorization: Bearer $TOKEN" -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
# 404
```

## Configuration

See [`configs/example.yaml`](configs/example.yaml). Two things worth knowing:

**Secrets are referenced, not stored.** Every credential names an `env:`,
`credential:` (systemd `LoadCredential`), or `file:` reference resolved at
startup. A config file that never holds a token cannot leak one into version
control.

**Scoping is per credential.** This agent reaches `/mcp/cnmaestro` and nothing
else:

```yaml
auth:
  static_tokens:
    - id: chatgpt-cnmaestro
      secret_ref: env:MCPD_TOKEN_CHATGPT
      principal: svc:chatgpt
      role: operator
      plugins: [cnmaestro]
```

## Development

```bash
make check   # fmt, vet, test, dependency pinning
make race    # tests under the race detector
make docker  # build the image
```

`make verify-deps` exists because `modernc.org/sqlite` requires
`modernc.org/libc` at the exact version in its own `go.mod`. A mismatch fails
at runtime rather than at build time, so it is checked in CI rather than left
to chance.

## Architecture

The full pre-implementation review — component diagram, plugin contract, state
machine, schema, outbox failure analysis, and the cnMaestro API findings — is
in [`docs/architecture.html`](docs/architecture.html).
