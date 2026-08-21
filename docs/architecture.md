# Architecture

How mcpd is put together, and why. This is the developer document; the
[README](../README.md) covers running it.

## Shape

One binary, two listeners, one database.

```
ChatGPT ──── Secure MCP Tunnel (outbound) ────┐
                                              ▼
browser ──── :8081 dashboard ──────────────► mcpd ──► SQLite
scripts ──── :8080 /mcp/{plugin} ─────────►         └► plugins
```

The MCP listener and the dashboard are separate ports on purpose. They have
different audiences and different exposure, and a firewall rule can only tell
them apart if they are separate listeners.

The tunnel runs inside the process and reaches the MCP server over an in-memory
transport, so there is no socket for it and no second credential. mcpd never
needs an inbound port, public DNS, or a NAT rule.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/mcpd` | Entry point, flags, `-check`, `init` |
| `internal/app` | Wiring and lifecycle: builds everything, owns shutdown |
| `internal/mcp` | The MCP host — routing, authentication, per-plugin endpoints |
| `internal/admin` | Dashboard JSON API and the embedded SPA |
| `internal/operations` | The approval engine: state machine, policy, executor |
| `internal/plugins` | Plugin registry, tool attachment, approval tools |
| `internal/auth` | Principals, roles, capabilities, static tokens |
| `internal/auth/users` | Accounts, passwords, browser sessions |
| `internal/storage/sqlite` | Schema, migrations, every transaction |
| `internal/tunnel` | The embedded OpenAI tunnel client, one per connector |
| `internal/settings` | Runtime configuration in the database |
| `internal/messaging` | In-process bus and outbox publisher |
| `internal/servertls` | Self-signed CA and certificate issuance |
| `internal/config` | File and environment configuration, validation |
| `sdk` | Building out-of-process plugins |

## The load-bearing decisions

**SQLite is the only authority.** Whether a change was approved is answered by
a row, never by a message. Every executor reloads and revalidates before
acting, so a lost, duplicated, or forged event costs latency at worst.

This is also why there is no broker. On one node a message bus carries a
wake-up signal from the process to itself, which an in-process channel does for
free. `internal/messaging` is that channel plus a transactional outbox; nothing
about the design assumes it stays in-process, and the outbox is what makes
swapping it a configuration change rather than a rewrite.

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
Everything else returns 404 rather than 403, so an agent scoped to one
integration cannot discover which others are deployed.

## The mutation state machine

```
                  ┌─────────────► rejected
                  │
draft ──► pending_approval ──► approved ──► executing ──┬──► succeeded
                  │                  │                  ├──► failed
                  │                  │                  └──► indeterminate
                  ├──► expired       └──► expired
                  └──► cancelled
```

Terminal: `succeeded`, `failed`, `rejected`, `expired`, `cancelled`.

`indeterminate` is deliberately **not** terminal. It means execution began and
the outcome is unknown, which is resolvable by observation rather than final.
Anything that treats it as settled — a retry, a re-proposal of the same intent
— risks applying a change that already landed.

Each transition is guarded in SQL. The claim into `executing` carries three
conditions in its `WHERE` clause: still approved, payload hash unchanged,
approval not expired. Anything else matches zero rows and the caller learns it
lost the race. That is what makes execution at-most-once.

## Approval, and where the human is

Leaving the conversation to approve every routine change is friction that gets
worked around. So mcpd asks in the conversation when it can: the MCP
specification's elicitation lets a server put a question to the user through
the client, and the answer returns as a real user action rather than a model
decision. `internal/plugins/elicit.go` raises it; `ApproveInline` records it.

`approval.inline_max_risk` is the ceiling. Above it the shortcut is withheld,
not the decision — the assistant has to show the change in full and be told
explicitly, then call the approve tool.

Enforcement is the same row either way. A client that cannot elicit gets the
two-step flow rather than an unguarded write; there is no path that skips it.
Tool annotations (`readOnlyHint`, `destructiveHint`, `openWorldHint`) tell a
client how to frame the call, which decides whether a person is *shown* a
change — never whether it may happen.

## Identity

Two kinds of caller, and they are not modes that exclude each other.

**People** sign in to the dashboard with an email and password, and hold a
session in an HttpOnly cookie the page cannot read. Writes carry a CSRF token
in a header: a cookie travels on a request another site caused, a header
cannot be set cross-origin, and that difference is the whole defence. Rights
are re-read per request, so disabling an account ends it immediately.

The first account is created from the dashboard. An instance with none offers
to create one and the registrant becomes administrator; the emptiness check
runs inside the write transaction, so two browsers racing an unclaimed instance
produce one administrator and one refusal.

**Machines** present a static bearer token from `auth.static_tokens`. The
tunnel presents nothing — it authenticates to OpenAI's control plane with a
runtime key and carries its identity from configuration.

Roles are `user` and `admin`. A user reads, proposes, and approves; an admin
additionally changes settings, makes tunnels, manages accounts, and clears
history. Capabilities (`read`, `propose`, `approve`, `admin`) are what code
checks — never the role directly.

mcpd is not an OAuth authorization server. It was, and the endpoints were
unreachable in the deployment they existed for: signing in through a tunnel
needs mcpd reachable from the public internet, which is the one thing a tunnel
avoids. OpenAI's documentation states the authorization server "is not
automatically tunneled".

## Storage

Tables: `operations`, `operation_transitions`, `execution_attempts`,
`idempotency_records`, `outbox_events`, `audit_events`, `audit_prune_gate`,
`plugin_state`, `settings`, `settings_history`, `users`, `user_sessions`.

Migrations are forward-only and checksummed; a changed file that has already
run is an error rather than a silent divergence. There is no down path —
rolling a schema change back on a database holding approved operations is a
data-loss decision, not an automated one.

`audit_events` is hash-chained and append-only, enforced by triggers. Pruning
opens `audit_prune_gate` for the duration of one transaction; without the gate
open the table refuses deletion, including from a `sqlite3` prompt.

Two idempotency mechanisms, and they must agree about time.
`idempotency_records` carries a TTL and implements the semantic: an identical
replay inside the window returns the original operation, and the same key with
a different body is refused. `ux_operations_idem` is a *partial* unique index
scoped to live states — one live operation per intent, while a settled one may
be proposed again. It was once permanent and blind to state, which made an
expired proposal un-retryable forever.

## Plugins

In-process plugins register in `registerPlugins`; the switch is the complete
list a binary can serve. Out-of-process plugins are ordinary programs speaking
the `sdk` protocol over stdio, mounted from the plugins directory.

A mutation declares its target, its desired state, and how to observe the
result. The host plans against live upstream state, freezes the payload,
executes at most once, then re-observes and compares.

The rule that matters most: if `Apply` cannot establish whether its write
landed, it must return `sdk.Indeterminate`. Anything else tells the host the
write did not happen and permits a retry that applies the change twice.

## Tunnels

One tunnel carries one address, so it is one connector in ChatGPT. A per-plugin
tunnel scopes by the principal it carries, not by URL — every tunnel binds the
in-process MCP server, so there is no URL to scope.

A tunnel's identity must not be written into a shared server. `AggregateServer`
caches by plugin set, which is right when identity arrives per request and
wrong for a caller carrying one, so a tunnel builds its own server. Getting
this wrong stacked middleware on every reconnect and let the first principal
answer for everyone.

## Development

```bash
make check   # fmt, vet, test, dependency pinning
make race    # tests under the race detector
make web     # rebuild the dashboard bundle
make build   # dashboard, then binary
make docker  # build the image
```

The dashboard is a Vite/React app in `web/`, built into `internal/admin/dist`
so `go:embed` can reach it. It is a build artifact — changing the UI means
rebuilding the bundle before the binary serves it.

`make verify-deps` exists because `modernc.org/sqlite` requires
`modernc.org/libc` at the exact version in its own `go.mod`. A mismatch fails
at runtime rather than at build time, so it is checked in CI.

The container's data directory is bind-mounted at `./.data`. The leading dot is
load-bearing: `cmd/go` skips dot-prefixed directories, and mcpd's TLS material
is mode 700 owned by the container user, so a visible `./data` breaks
`go build ./...` on any machine that has run the container.

## Deployment

Primary target is a Linux VM with systemd. [`deploy/mcpd.service`](../deploy/mcpd.service)
is a hardened unit: dedicated user, `ProtectSystem=strict`, all capabilities
dropped except `CAP_NET_BIND_SERVICE`, and a syscall filter.

The container image is distroless, non-root (uid 65532), read-only root
filesystem, with `/tmp` on tmpfs.

## Plugins are not architecture

What an integration does belongs with the integration. Each has its own
document when it needs one — see [cnmaestro.md](cnmaestro.md) — because the
API a plugin talks to changes on someone else's schedule, and mixing that into
the host's design makes both harder to read.

What is architectural is the contract every plugin meets.

**A type, and its instances.** A plugin *type* is an integration the binary was
built with; an *instance* is one configured copy of it. The config key is the
instance name and `type` says what it is an instance of, defaulting to the key.
Two instances are two plugins as far as the host is concerned — two endpoints,
two entries in a credential's plugin list, two connectors, and operations that
say which one acted — because the name is already the identity everywhere
downstream. That is also why an instance argument on each tool was the wrong
design: access is granted per plugin, so a shared endpoint could not express
"this agent reaches one and not the other".

**Four things a plugin can declare.**

| | what it is |
|---|---|
| Tool | an action a model chooses and reasons about choosing |
| Mutation | a write, which becomes propose/approve rather than a tool that writes |
| Resource | reference material read by address, kept out of the tool catalogue |
| Prompt | a named way of asking something useful; returns text, performs nothing |

All four pass the same authorization gate. A resource that skipped it would be
a way around per-plugin scoping, and a prompt that acted would be a tool
wearing a name that hides it from every check tools go through.

A tool may raise its capability above read — for the read that is not merely a
read, where seeing something is itself the privilege — and may declare a rate
limit, per tool rather than per plugin, because the expensive call is usually
one endpoint rather than an integration.

**Settings belong to the plugin, resolution belongs to the host.** A type
declares its fields; the host namespaces them per instance, validates them,
encrypts the secrets, renders the form, and hands back resolved values. A
plugin never reads a file or an environment variable. Values resolve store,
then file, then default — the store winning because a value changed in the
dashboard has to beat the one the host started with.

A plugin whose credentials are entered in the dashboard cannot refuse to start
without them: a host that will not start is a dashboard nobody can open to
enter them. Structure is validated at construction, credentials at `Start`, so
an unconfigured instance mounts, shows its form, and reports what is missing.
