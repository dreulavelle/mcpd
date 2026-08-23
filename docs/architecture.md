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
| `internal/mcpservers` | server.json, and the snapshot of a remote server's tools |
| `internal/plugins/mcpremote` | Mounting a remote MCP server as a plugin |
| `internal/registry` | Browsing a public catalogue of MCP servers |
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

**A claim of verification has to be earned.** Three proofs make a change a
*reviewed change*: the approver saw the exact fields, drift between proposal
and execution is detectable, and the outcome is confirmed by re-reading the
target. A mutation declares whether it can offer the third (`Verifiable`) and
supplies the second by declaring preconditions. Anything short of all three is
a *gated call* — a human authorised it and it happened, and that is the whole
of the evidence. The two words are kept apart in the note the model reads and
in what the API returns, because the second borrowing the first's credibility
is exactly how a system ends up claiming more integrity than it has.

An operation that cannot be verified settles with `outcome_verified` null, not
false. "Not checked" and "checked and did not match" are different facts, and
collapsing them is what let the executor report a confirmation it never made.

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

**A display name is a rendering, never an identity.** An account is identified
by its address, and that is what every audit record, every guard and every
grant is keyed on. The name is optional, falls back to the address when empty
so nothing renders blank, and is resolved when a page is drawn rather than
stored beside the thing it describes — a record keyed on a value its own
subject can edit would be a record of nothing.

Because identity does not depend on it, an account may set its own name without
`admin`. `PATCH /api/account` carries no identifier and can only ever edit the
account the request authenticated as, so there is no check to get wrong;
naming somebody else is still `PATCH /api/users/{id}` and still `admin`. What
bounds the value is a length, a refusal of control and invisible-formatting
characters — a newline breaks a log line in two, and a bidirectional override
renders a name as something it is not — and a condition in the `WHERE` clause
of the write refusing a name that is another account's address.

mcpd is not an OAuth authorization server. It was, and the endpoints were
unreachable in the deployment they existed for: signing in through a tunnel
needs mcpd reachable from the public internet, which is the one thing a tunnel
avoids. OpenAI's documentation states the authorization server "is not
automatically tunneled".

## Storage

Tables: `operations`, `operation_transitions`, `execution_attempts`,
`idempotency_records`, `outbox_events`, `audit_events`, `audit_prune_gate`,
`plugin_state`, `settings`, `settings_history`, `users`, `user_sessions`,
`mcp_servers`, `mcp_server_tools`.

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
the `sdk` protocol over stdio, mounted from the plugins directory. A third
kind is a remote MCP server, described below.

A mutation declares its target, its desired state, whether observing the result
confirms it, and how to observe it. The host plans against live upstream state,
freezes the payload, executes at most once, then re-observes and compares — the
last step only when the mutation said it would mean something. Verifiability is
declared rather than inferred from an empty desired state, because that field
is ambiguous: for a delete it means "the target should be gone", which is a
real thing to observe, and for a write that cannot be read back it means
nothing at all.

The plan travels from `Plan` to `Apply` in the argument. Nothing may key it on
the parameters instead: two live proposals of the same change share those, and
whichever executes first would consume the other's plan.

The rule that matters most: if `Apply` cannot establish whether its write
landed, it must return `sdk.Indeterminate`. Anything else tells the host the
write did not happen and permits a retry that applies the change twice.

## Remote MCP servers

A remote MCP server is somebody else's, reached over the network. It is mounted
as an ordinary plugin — same endpoint shape, same per-plugin scoping, same
authorization gate, same audit — and the difference is trust, carried by
`Descriptor.Runtime`. Everything mcpd was built with is `builtin`; only this is
`mcp`.

Three of the host's rules change for that runtime, and each is applied where
the rule lives rather than by the code that builds one:

- **No mutations.** The registry refuses one. There is no propose/approve story
  for a third party's tool: the host cannot plan against its state, cannot
  freeze a payload it does not understand, and cannot re-observe the result.
- **Tool names follow the specification, not the house style.** `getWeather`,
  `search.docs` and `read-file` are all valid upstream, and the house rule
  rejects every one. Names are passed through unchanged — a normalised name is
  one the far end does not answer to — bounded only by the 128-character limit
  on the prefixed name.
- **Attachment is per tool.** One malformed descriptor out of three hundred
  costs that tool. The far end's catalogue is not something an operator can fix.

**Register reads SQLite, never the network.** This is the load-bearing decision.
The tools a remote server offers are snapshotted at discovery and mounted from
that snapshot, so a host restarting while the far end is down comes up serving
exactly what it served before and reports itself unhealthy. Calling `tools/list`
at boot would give a host with no tools and a model that reasonably concludes
the integration was removed.

**Every administrative act is in the audit trail.** Importing a server, running
discovery, classifying a tool, turning a server on or off, and removing one
each append to the hash-chained `audit_events`, inside the transaction that
performed them, naming the principal who acted. Enabling a tool is a privilege
grant — it hands every caller of that plugin a path into a third party's code —
so it belongs where privilege grants are recorded rather than in the settings
history. A toggle that changes nothing writes nothing: a trail that records
non-events is one nobody reads carefully. Reads are deliberately not audited
per call; this is about state changes.

**Nothing the server says is authority.** `tools/list` is a claim. A tool
arrives `pending` and is not served until an administrator classifies it, and
what they classify is a *descriptor* rather than a name — `descriptor_hash` is
in the `WHERE` clause of every state change, so a tool whose schema changed
underneath an approval cannot inherit it. Its annotations are hints the MCP
specification itself says not to trust, so nothing branches on them. An input
schema that is not an object disqualifies the tool: substituting a permissive
one, which the out-of-process adapter does for a binary the operator dropped in
themselves, would throw away the only argument validation there is.

Lifecycle: import a `server.json` → discover → classify each tool → it mounts.
The document is stored verbatim and validated against a **vendored** copy of the
schema; nothing is fetched at runtime, and a document declaring a `$schema` this
build does not read is refused rather than parsed optimistically. Its inputs
become settings fields, so a credential is typed into the dashboard, encrypted
at rest, and resolved store-then-file-then-default like every other — never read
out of the document.

**A credential goes to the address it was configured for, and nowhere else.**
The configured headers are pinned to the configured origin — scheme, host and
port — in two places independently: the client refuses a redirect off it, and
the transport that injects them checks again before it does. Go's own defence
does not reach this. The standard library strips `Authorization` and `Cookie` on
a cross-domain redirect, but only for headers set on the original request; it
cannot see one a RoundTripper adds per hop, which is what this one does. Without
the pin, a server answering `302 Location: https://attacker.example/` — after a
compromise, a DNS change, or an expired domain someone else registered — is
handed the operator's API key. The same check refuses a hop that reaches further
into the deployment's own network than the configured endpoint already did, so a
public server cannot steer this host at a metadata service. It is judged against
the endpoint rather than absolutely, because a server on loopback or on the
LAN is an ordinary thing to configure and is not made safer by being refused.

Rate limiting is per server as well as per tool, because thirty tools behind one
address are one upstream.

## The catalogue

Hand-authoring a `server.json` to add a server somebody else already published
is copying. `internal/registry` browses the official MCP registry so an
operator can pick one instead.

**It finds documents; it does not install them.** Selecting an entry hands its
`server.json` to the same import endpoint a paste goes through, and everything
downstream is unchanged: the same validation, the same derived settings, the
same discovery, the same per-tool approval. There is no second import path,
which is the only way to be sure the catalogue cannot become a way around one
of those steps.

That is also what decides whether an entry is offered at all. Addability is not
"does it have a `remotes` array" — it is `mcpservers.Parse` returning nil, the
same call the import endpoint makes. A document with remotes this host will not
dial, or a credential written into its own text, imports as a refusal; offering
an Add button for one would be offering a button that fails.

**Remote servers only.** Roughly a tenth of the registry is published solely as
something to run locally, which this host does not do. Those are listed with
the reason rather than filtered out, because "why is the thing I came for not
here" is a worse question than a greyed-out row that answers it.

**Nothing about the catalogue is on a request path or a startup path.** The
client is constructed at boot and reaches nothing; the first fetch happens on
the first request that asks for one. Answers are cached with a TTL, and a
catalogue that cannot be reached serves what it last saw, marked stale with the
time it was fetched. A third party being down is not this deployment's failure
and is not worth a page that will not render — but neither is it worth
pretending the data is current.

**The registry's content is a third party's text, arriving in whatever quantity
they choose to send.** The response is bounded before it is decoded, the entry
count per page is capped, and every field is bounded and stripped of control
and invisible-formatting characters before it is stored or returned.

**Deduplicate by name.** The registry holds every version of every server and
returns them all unless asked otherwise. The query asks for `version=latest`
and the deduplication runs anyway: "the far end promises one row per name" is
exactly the kind of promise whose failure shows up as a catalogue page listing
the same server four times.

Browsing takes `admin`. Everything it returns is public; the privilege is
making this host reach a third party from inside the deployment. Nothing about
it changes state, so nothing about it is audited — importing what it found is,
like any other import.

The client is behind an interface so a second catalogue can be added without
touching a caller. There is one implementation, and adding another is a
decision nobody has made.

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

**Behind a reverse proxy, mcpd should not serve TLS itself.** The ordinary
shape is an FQDN with Caddy, nginx or Cloudflare terminating TLS and forwarding
plain HTTP, and the right setting for that is `tls.mode: off`; `self-signed` is
for reaching mcpd directly, where the alternative is a browser warning on every
visit. Set `server.public_url` to the address people actually type. It is not
cosmetic: mcpd is reached over plain HTTP in that shape, so `r.TLS` is nil and
the configured scheme is the only way it can know the session cookie needs
`Secure`. `X-Forwarded-Proto` deliberately does not count — a header is set by
whoever is talking to this process, and nothing here can tell a proxy's from a
caller's.

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

**Instances come from two places.** The configuration file, and the settings
store where the dashboard writes them. The store layers over the file, and an
instance knows which it came from — the dashboard will not delete a file-defined
one, because it would return on the next start and read as the delete having
failed.

Adding an instance records intent; it does not mount. A plugin is built once,
at startup, from the settings it had then, so the dashboard says a restart is
needed rather than showing an instance whose tools never appear. Removing one
takes its settings with it, so a name reused later cannot silently inherit
someone else's credentials.
