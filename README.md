# mcpd

An extensible host for Model Context Protocol integrations. One binary serves
many plugins, each on its own endpoint, with every infrastructure mutation
gated behind a durable approval workflow.

```
                         ChatGPT
                            │  HTTPS + OAuth
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
       /mcp/cnmaestro  /mcp/netbox  /mcp/proxmox
              └─────────────┼─────────────┘
                            ▼
                          mcpd
                     (single binary)
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
          plugins        SQLite      dashboard
```

## Why it is built this way

**SQLite is the only authority.** Whether a change was approved is answered by
a row in a database, never by a message on a bus. Every executor reloads and
revalidates before acting, so a lost, duplicated, or forged event costs latency
at worst. This is also why there is no broker: on one node, a message bus would
carry a wake-up signal from the process to itself.

**Nothing writes without an approval it can prove.** A mutation's payload is
hashed at proposal and frozen. Before execution the hash is recomputed and
compared inside the same statement that claims the operation, so a tampered
payload cannot execute even if it slipped past every check above it.

**Proposing and approving are separate acts.** A model can describe a change;
it cannot authorise one. Approval references a stored operation by id and
cannot carry parameters, so the thing approved is exactly the thing reviewed.

**An ambiguous outcome is recorded as ambiguous.** If the process dies between
issuing an upstream write and recording its result, the operation lands in
`indeterminate`, not `failed`. Calling it a failure invites a retry, and the
retry double-applies the change.

**Access is per plugin.** A credential lists the plugins it may reach.
Everything else returns 404 — not 403 — so an agent scoped to one integration
cannot discover which others are deployed.

## Quick start

### Docker

```bash
cp .env.example .env
# generate a token:  openssl rand -base64 32
$EDITOR .env
docker compose up -d
```

The dashboard is on **port 80**; the MCP endpoint is on loopback:8080.

To reach it from ChatGPT over HTTPS without opening a port, create a Cloudflare
Tunnel pointing at `http://mcpd:8080`, put its token in `.env`, and run:

```bash
docker compose --profile tunnel up -d
```

### From source

```bash
make build          # builds the dashboard, then the binary
./bin/mcpd -config configs/example.yaml
```

Validate a config without starting anything:

```bash
./bin/mcpd -config /etc/mcpd/config.yaml -check
```

## Connecting ChatGPT

Set `auth.mode: oauth` and a bootstrap administrator, then point ChatGPT's
developer-mode connector at `https://your-host/mcp/cnmaestro`.

mcpd is its own OAuth authorization server, so nothing else needs to be
deployed. ChatGPT discovers the endpoints, registers itself, and presents a
consent screen where you sign in and choose what the connector may reach.

The consent screen states plainly which plugins the token will reach and
whether it will be able to propose changes. A user can never delegate more than
they hold: an operator cannot confer approval rights, and nobody can grant a
plugin they lack.

## The approval flow

```
  cnmaestro_set_radio_channel      →  operation_id, state=pending_approval
       (nothing has changed)

  cnmaestro_approve_operation      →  state=approved
       (a human decides)

  executor                         →  reload, revalidate, claim, apply, verify
       (at most once)              →  state=succeeded, verified=true
```

Between approval and execution the host recomputes the payload hash, re-plans
against live upstream state, and compares preconditions. If the target changed
after approval, the change is refused rather than applied over someone else's
work.

## Writing a plugin

A plugin is an ordinary Go program:

```go
func main() {
    p := sdk.New("weather", "1.0.0", "Weather", "Reads local weather.")

    sdk.Tool(p, sdk.ToolSpec{
        Name:        "forecast",
        Description: "Get the forecast for a city.",
    }, func(ctx context.Context, in ForecastInput) (Forecast, error) {
        return lookup(ctx, in.City)
    })

    sdk.Serve(p)
}
```

Build it into the plugins directory with a manifest and restart:

```bash
go build -o /var/lib/mcpd/plugins/weather/weather ./cmd/weather
echo '{"name":"weather","exec":"weather"}' > /var/lib/mcpd/plugins/weather/plugin.json
```

mcpd mounts it at `/mcp/weather`. See [`examples/hello`](examples/hello) for a
complete plugin including an approval-gated mutation, and the [`sdk`](sdk)
package docs for the mutation contract.

The one rule that matters most: if a mutation's `Apply` cannot establish
whether its write landed, it must return `sdk.Indeterminate`. Anything else
tells the host the write did not happen, and permits a retry that applies the
change twice.

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

## Deployment

Primary target is a Linux VM with systemd. [`deploy/mcpd.service`](deploy/mcpd.service)
is a hardened unit: dedicated user, `ProtectSystem=strict`, all capabilities
dropped except `CAP_NET_BIND_SERVICE` for the dashboard's port 80, and a
syscall filter.

Terminate TLS at a reverse proxy and bind mcpd to loopback. The container image
is 24 MB, distroless, non-root, with a read-only root filesystem.

## Development

```bash
make check   # fmt, vet, test, dependency pinning
make race    # tests under the race detector
make web     # rebuild the dashboard bundle
make docker  # build the image
```

`make verify-deps` exists because `modernc.org/sqlite` requires
`modernc.org/libc` at the exact version in its own `go.mod`. A mismatch fails
at runtime rather than at build time, so it is checked in CI.

## Architecture

The pre-implementation review — component diagram, plugin contract, state
machine, schema, outbox failure analysis, and the cnMaestro 6.3.0 API findings —
is in [`docs/architecture.html`](docs/architecture.html).

## Status

Working: the MCP host with per-plugin endpoints and scoping; OAuth 2.1
authorization server; the approval engine end to end; SQLite storage with a
hash-chained audit trail; the cnMaestro plugin; the operator dashboard; and the
out-of-process plugin SDK.

Not yet validated: cnMaestro's write path has been built against the published
6.3.0 specification and tested against a fake controller, but not against real
hardware. In particular, whether `PUT /devices/{mac}` merges or replaces the
`overrides` object is undocumented; mcpd resends every override it read, which
is correct under either behaviour, but this should be confirmed before the
first production write.
