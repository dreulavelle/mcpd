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
| `internal/registry` | Browsing the public catalogues of MCP servers |
| `internal/messaging` | In-process bus and outbox publisher |
| `internal/cachestore` | The bounded, timed map every cache is built on |
| `internal/servertls` | Self-signed CA and certificate issuance |
| `internal/config` | File and environment configuration, validation |
| `sdk` | Building out-of-process plugins |

## The load-bearing decisions

**What belongs here is settled by a test, not by taste.** mcpd is a control
plane for MCP servers, and the test for any feature is: does it help somebody
decide *which MCPs run, who can reach them, and what happened?* Rendering an
integration's own data — a device topology, a metrics dashboard — fails it.
Plugin tools return data to the model, not to mcpd's UI.

This is written down because its absence let a bad idea get as far as a
proposal. The idea was plausible, it was about a real integration, and nothing
in this document said no to it — which is what a missing test looks like from
the inside.

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
a *gated call* — it was authorised and it happened, and that is the whole of
the evidence. ("Authorised" rather than "a human said yes": a standing rule can
authorise a change with nobody being asked. Who authorised it is a separate
fact from what can be proved about it, and the two are kept apart.) The two words are kept apart in the note the model reads and
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

## Asking about everything is the same mistake

Risk was computed on every proposal, stored, displayed, and consulted by
nothing. Every mutation was equally consequential as far as the gate was
concerned, which means a change of nothing much interrupts a person exactly as
hard as rebooting a site does — and a gate that inconvenient is one people
route around. Wanting an agent to be able to nudge a channel without a human
in the loop is not wanting less safety; it is wanting the interruption to be
worth something when it happens.

So a **standing rule** can authorise a class of change in advance.
`internal/operations/autoapprove.go` is the whole of it: three selectors, a
ceiling, and a note.

| | |
|---|---|
| `plugin` | which integration, or `*` |
| `action` | which mutation, or `*` |
| `principal` | whose proposals, or `*` |
| `max_risk` | the highest risk it authorises; **empty authorises nothing** |

**Most specific wins, and only one rule decides.** Plugin outranks action
because an action name means nothing without the plugin it belongs to, and both
outrank principal — a rule carving an action out is a statement about the
change itself, "rebooting a device is never automatic", and must not be
defeated by a broad grant to whoever happens to be asking. Two rules on one
scope are refused when the set is stored, because picking between them would be
right only by accident. An empty ceiling is not a disabled rule: it authorises
nothing, and because a more specific rule wins outright it is how one action is
carved out of a permissive plugin-wide rule.

**The default is to ask about everything.** The zero value of the policy is no
rules, and no rules means every mutation goes to a person exactly as it did
before this existed. A policy that loosens on upgrade is the wrong direction to
be wrong in, so nothing changes until an administrator writes a rule. Rules
have no fallback in the configuration file for the same reason: the one setting
whose effect is to skip a human belongs where the change is recorded against
whoever made it, and where the dashboard can show the whole set.

**Three things no rule may authorise.** A mutation that declares no way back
(`MutationSpec.Reversible`), because the case for a standing authorisation is
that a mistake is cheap to correct and it does not survive the absence of a
correction. An unrecognised risk classification, because an unknown is exactly
the thing to put in front of someone. And `critical` — a level an operator can
quietly opt out of is not a level.

**Risk is raised before the policy sees it.** The ceiling is compared against
the risk as it finally stands: the mutation's declaration, raised by the plan
for these specific parameters, raised again by any operator override. A plan
that reclassifies a change upward puts it back in front of a person even though
the proposal qualified without it. The same re-plan runs immediately before
execution, and where a *rule* authorised the change and the re-plan raises the
risk, the executor refuses (`RISK_RAISED`) — the rule authorised a change of
one severity and the target now says it is another, and nobody ever looked.
Where a *person* approved, the same raise changes nothing: they saw this change
and said yes to it, and treating a reclassification as a withdrawal would make
every approval provisional.

**What you lose is the interruption, not the evidence.** An auto-approved
operation is an ordinary operation. The row is written, the payload frozen and
hashed, plan/apply/observe runs, drift is checked, the outcome verified where
the mutation can prove one, and every transition is in the hash-chained trail.
It skips the ask and nothing else. So the property this rests on survives with
one word changed: nothing writes without a **recorded authorisation** it can
prove.

Which is why the record has to name the rule. `operations.authorized_by_rule`
holds it, written in the same guarded `UPDATE` as the approval and immutable
afterwards, and the audit entry carries the rule's scope, ceiling and note in
full — a rule can be edited or deleted, and an entry naming an identifier whose
meaning has since changed would describe an authorisation that never happened.
The approver is `system:policy`, never the principal who proposed: attributing
it to them would say a person approved their own write, which is the one thing
that did not happen. "Auto-approved" with nothing naming the rule is exactly
the unprovable approval this project exists to avoid.

**Assurance is orthogonal and stays orthogonal.** *Nobody was asked* and
*nothing can be proved* are different facts. An auto-approved change that
declares preconditions and can be re-read is still a `reviewed_change`; one
that cannot is still a `gated_call`. Collapsing the two would let the vocabulary
that exists to stop a claim being overstated start overstating one.

**Auto-approval does not consult `CapApprove`.** The authority is an
administrator's rule, not the proposer's standing; what bounds the proposer is
`CapPropose`, checked where every proposal is. Writing rules is `CapAdmin`;
reading them is `CapRead`, because "why was I not asked" is a question an
operator has to be able to answer.

**A rule removes an interruption, and something else has to take over the
backpressure.** Before a rule existed, a runaway agent could only pile up
proposals somebody would decline; under one it lands writes at whatever rate it
can call. The human in the loop was doing that job as a side effect, and a rule
is a decision to stop paying for it.

So a mutation now carries a rate limit of its own, and unlike a read tool's it
is never absent: `MutationSpec.RateLimit` defaults to one proposal a second and
a plugin may raise or lower it knowing what its own upstream costs. Unbounded is
not a defensible zero value for a write. A read tool that declares no limit
costs an upstream a request; a mutation that declares none costs it a *change*,
and under a rule nobody is asked first.

**The limit is per caller; the read tool's is global.** The difference is not an
inconsistency. A read tool's limit protects an upstream's quota, which is a
shared resource no caller has a claim on. A mutation's limit exists because one
agent can loop, and a single global budget would let that agent spend it and
leave the operator's own corrective change refused — and the corrective change
is the one that stops the runaway. What protects the target from many callers at
once is where it has always been: the plugin's client, which knows what its API
can take.

**A refusal costs nothing a retry would find spent.** It is checked after the
authorization gate and before everything else — before the plan, which reads
upstream, and before the operation is recorded. So a refused proposal leaves no
row, spends no idempotency key, and makes no upstream call. That matters more
here than for a read: a refusal that consumed the idempotency of the operation
it refused would make the retry the caller was told to make return the wrong
answer.

**A rule is decoded strictly, and a misspelled selector is an error.** An
omitted selector means "anything", which is the convenience that makes
strictness load-bearing: `{"principle": "svc:agent"}` would otherwise be
discarded silently and the real principal default to every principal, turning a
deliberately narrow rule into a global one with nothing saying so. An unknown
field, an explicit `null`, and an empty selector are all refused, and the check
lives on the rule type rather than in the handler — a `json.Decoder`'s
`DisallowUnknownFields` does not reach inside a custom `UnmarshalJSON`, so the
type is the only place that covers the API, the settings store at startup, and
a restore alike.

Rules are read and written at `GET`/`PUT /api/approval-policy`, and
`POST /api/approval-policy/evaluate` answers "which rule would apply, and why"
before a change is proposed rather than only afterwards from the record. The
shapes are in [approval-policy.md](approval-policy.md).

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

The rule is enforced twice, and the second time is not belt-and-braces. It is
checked on the way in, and checked again by the one function every render goes
through, because the column is older than any rule about what may go in it and
a database may hold a name written when nothing was checked. A value the rules
now refuse renders as the address instead. The schema cannot cover this and it
would be dishonest to pretend otherwise: a `CHECK` can express the length, and
enumerating the format characters in SQL would catch a score of the hundred and
seventy in the category and drift from the Go rule the first time either
changed. Migration `0011` therefore normalises the length retroactively and
nothing else, and re-checking on read is what makes that sufficient. The stored
value is left alone rather than corrected, so its owner can see what is there
and replace it.

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

## Telling what it is doing

`/health/live` and `/health/ready` are on the MCP listener because a load
balancer in front of that port has to reach them without a credential, and they
carry aggregate state and nothing else for the same reason.

`/metrics` is not on that listener. It is on the **dashboard** one, and the
difference is deliberate: the MCP port is what a third party reaches through a
tunnel, and metrics name every mounted plugin, every tool, how long each named
upstream takes to answer, and how often each fails. That is exactly the
operational detail the readiness probe is careful not to carry. The dashboard
listener already has the right audience — operators, on an internal interface —
and the rest of the operational detail already lives there.

It takes `read`, which is what a read of this host's own state takes
everywhere else here, and a Prometheus satisfies it with a static token like
any other machine caller. `metrics.public` drops the check for a deployment
that has already fenced the port off to a monitoring network; it is off by
default and config validation says plainly what turning it on means. Switching
metrics off leaves the route answering 404 rather than the dashboard's own
shell, so a scrape config pointing at a host that is not serving them fails
instead of quietly parsing HTML.

**Some numbers are not this process's to keep.** How many operations are in
each state is answered by SQLite, and a counter incremented in Go would
disagree with it after every restart and every prune — and would never mention
the row that has been sitting in `indeterminate` since Tuesday. Those are read
when a scrape arrives, bounded by their own timeout so a busy database costs
the series rather than the response. Counters for things that only happen in
memory — a tool call, a refused proposal, a cache hit — are ordinary counters.

Every series is there because somebody asks the question it answers; the list
and the question each one is for are in `internal/observability/metrics.go`.
Two cardinality rules hold throughout: a label is a class and never an
identifier — a metric labelled with a device address is a new time series per
device — and a plugin is handed an interface narrow enough to report its own
cache and its own upstream latency and nothing else, so an integration cannot
invent series this host then has to carry.

## Storage

Tables: `operations`, `operation_transitions`, `execution_attempts`,
`idempotency_records`, `outbox_events`, `audit_events`, `audit_prune_gate`,
`plugin_state`, `plugin_overrides`, `settings`, `settings_history`, `users`,
`user_sessions`, `mcp_servers`, `mcp_server_tools`.

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

**A subprocess's stdio is multiplexed, and a caller's deadline is its own.**
One goroutine owns the pipe's read side for the process's whole life and hands
each frame to the caller whose id it carries; the write lock covers the write
and nothing else. What that buys is not throughput so much as independence.
Holding one lock across the whole round trip made every caller queue behind the
one in flight — and worse, each of them had already started its own timeout
before joining the queue, so the first to acquire the lock immediately found its
deadline gone and killed the plugin to recover a pipe that was not blocked.
A slow call took down every other caller, including the ones about to succeed.

Now a timeout fails one call. The process is left alone, a late answer is
discarded by id rather than handed to the next caller, and a plugin that has
genuinely stopped answering fails every call and reports itself unhealthy. Two
things still end it: a frame over the cap, because the rest of the line has not
been read and the stream position is no longer known, and shutdown.

The write lock is a channel rather than a mutex, because it has to be possible
to give up on. A plugin that stops reading its stdin will eventually block a
writer, and a `sync.Mutex` would queue every other caller behind that with no
way out — which is the shape being removed. Acquiring selects on the caller's
context, so a caller that cannot get the pipe fails on its own deadline.

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
is copying. `internal/registry` browses the public catalogues of MCP servers so
an operator can pick one instead.

Four of them today.

| catalogue | what it is | default |
|---|---|---|
| `registry.modelcontextprotocol.io` | the official registry, where a publisher registers a server themselves | on |
| `api.pulsemcp.com` | PulseMCP's v0.1 sub-registry, ~22,000 servers, mostly mirrored from the official one | **off** |
| `docker/mcp-registry` | Docker's curated catalogue | on |
| `registry.smithery.ai` | Smithery's registry, ~10,500 servers, all hosted behind one gateway | on |

Docker's is built from
[docker/mcp-registry](https://github.com/docker/mcp-registry), which is MIT
licensed — the notice travels with the vendored fixtures and with every
document composed from an entry, in its `_meta`. Any of the four can be
switched off under `catalog:` in the configuration file; a deployment with none
gets an endpoint that says so.

Reading a catalogue is an HTTPS fetch of somebody's metadata. Nothing about
Docker's involves Docker: no daemon, no container, no image.

**A source is on by default when it is useful without a credential.** That is
the whole of the rule, and it separates the two new ones. Smithery's *servers*
need a key to dial, but its registry does not need one to browse — so an
operator with no Smithery account still gets ten thousand descriptions, a
search over them, and a row that says which ones would ask for a key.
PulseMCP's v0.1 API authenticates every request, so the same deployment would
get a page of 401s; a source that can only report its own misconfiguration is
worse to default to than an absence, and it is off until a key and tenant are
configured. Config validation refuses to start with it on and either missing,
because "mounted and failing every page" is the state nobody wants.

That is also why the obvious PulseMCP integration is not the one built. The
unauthenticated `v0beta` API — `count_per_page`, `offset`, a `remotes[]`
carrying `url_direct` and `cost` — is being switched off on a published
schedule that reached 50% of requests randomly failing in June 2026 and reaches
100% in September. Measured on 2026-08-23, three of six requests came back
`410 API_SUNSET`. Building on it would have shipped a source already half dead.

**Two catalogues speak one API, so there is one reader.** PulseMCP's v0.1
implements the Generic MCP Registry API — the same `{server, _meta}` rows,
`metadata.nextCursor` and pass-through `server.json` the official registry
serves. What differs between the two is a base URL, two credential headers and
the `_meta` key each writes its lifecycle facts under, which is a parameter
list rather than a second implementation. `generic.go` is the reader;
`official.go` and `pulsemcp.go` are its two configurations.

**Smithery's listing is bounded, and says so.** Its API reports ten and a half
thousand servers and then refuses to page past five hundred of them, whatever
page size is asked for — at `pageSize=100` `totalPages` is 5, at `pageSize=3`
it is 167, and both are the same five hundred rows. Search is not bounded that
way, so `?q=` is passed upstream rather than used to filter a page that was
already truncated: filtering locally would present the top five hundred as the
catalogue, and a search for a server at position nine thousand would come back
empty with nothing saying why. A browse carries a note on its `SourceStatus`
saying the listing stops and search reaches the rest, because a page whose last
row is the five hundredth looks exactly like the end of a catalogue.

Smithery's paging also repeats itself: those five hundred rows held two hundred
and sixty-nine distinct servers when measured, and pages one and two shared
thirty-nine. Page one is stable when refetched, so it is not jitter a retry
would fix — the ordering is by popularity and is not a total order. The window
is fetched whole (page one, then the rest concurrently) and deduplicated by
name before anything is paged out of it.

**Then it is ordered by use, which is the only real quality signal any of the
four publishes.** Every Smithery listing row carries a `useCount` — how many
times Smithery has been asked to call that server — and the numbers are not
close: the head of the catalogue is in the tens of thousands and the tail is at
zero. A default view of ten servers out of twelve thousand is a sample either
way, and "ten servers people use" is a far better sample than "ten servers".
`verified` breaks a tie, because Smithery vouching for a listing is worth
something between two servers nobody has called and nothing at all against one
with fifty thousand calls behind it; the name breaks the remaining ties, which
is what makes the ordering *total* — and a total order is what lets the cursor
be a resume point rather than an offset into a list that reshuffles. The cursor
is the rank key rather than the last name, for exactly that reason.

**There is no cross-source ranking, because there is no cross-source signal.**
The obvious next step is a merged "most used" order, and it cannot be built
honestly. The official registry and Docker publish no usage figure at all.
PulseMCP publishes one, but it is `visitorsEstimateMostRecentWeek` — unique
visitors to a listing page — which is not the same measurement as a count of
tool calls and does not become one by being divided by something. Two of four
sources silent and the other two counting different things is not a ranking
waiting for a normalisation; it is a normalisation this host would have to
invent and then present as a fact. So each source is ordered by the best signal
it actually has, `Multi` interleaves them round-robin, and the page says it is
a sample rather than a top ten.

**One Smithery key opens every Smithery server, and it still lives in the
per-server settings.** Every hosted server is at
`server.smithery.ai/{qualifiedName}/mcp`, streamable-http, `401 invalid_token`
without an `Authorization` header — so the composed document declares that
header with a `{SMITHERY_API_KEY}` placeholder behind it, marked secret, which
is the shape Docker's entries already produce. The key then arrives the way
every other credential does: typed into the dashboard, encrypted at rest,
resolved store-then-file-then-default, never written into the stored document.

Holding it once for the source instead was considered and refused. It would
have to be either substituted into the document to be used — a credential at
rest inside a stored, hashed document, which is the one thing the import path's
verbatim storage makes indefensible — or resolved at dial time from a store the
plugin does not belong to, which is a second credential path beside the one
every other plugin uses and a hole in per-plugin scoping. The cost of refusing
is real and is worth naming: an operator importing four Smithery servers pastes
the same key four times, into four fields that each say what they are for.

**It finds documents; it does not install them.** Selecting an entry hands its
`server.json` to the same import endpoint a paste goes through, and everything
downstream is unchanged: the same validation, the same derived settings, the
same discovery, the same per-tool approval. There is no second import path,
which is the only way to be sure the catalogue cannot become a way around one
of those steps.

That is also what decides whether an entry is offered at all. Addability is not
"does it have a `remotes` array" — it is the two calls the import endpoint
makes, both of them: `mcpservers.Parse` on the document, then `mcpremote.Fields`
on the result. The second is not redundant. `Parse` judges the document;
`Fields` derives the form an operator would fill in, and refuses things `Parse`
accepts — an input declaring choices whose default is not one of them, or a
field the settings catalogue will not take. Checking only the first is how this
offers an Add button that fails, which is the one thing it exists to prevent.

This is the reason `internal/registry` imports `internal/plugins/mcpremote`,
and so is not the leaf it otherwise would be. The alternative was to
re-implement the acceptance rule beside the catalogue, which is the same bug
with an extra copy of the code to keep in step.

**Remote servers only, and a listing does not show the rest.** Roughly half of
the official registry, three quarters of Docker's catalogue, and the servers
Smithery does not host are published solely as something to run locally — an
npm package, a container, a command. This host does not run those. Docker's
`type: server` and `type: poci` entries are exactly that case, and so is an
entry reachable only through an OAuth flow Docker's own gateway performs: this
host sends a credential an operator configured, and the entry does not say
which header would carry the one that flow obtains.

They used to be listed, greyed, with the reason, on the argument that "why is
the thing I came for not here" is a worse question than a row that answers it.
That was right at thirty rows a page and wrong at ten. An operator who used it
reported the noise as worse than the missing answer, and the arithmetic agrees:
a page of ten that spends five rows explaining refusals is a page of five. So
`Multi` drops them, server-side, *before* the paging — which is the only place
it can be done and still have ten rows mean ten usable rows.

Nothing about the machinery is weaker for it. Addability is still decided by
`mcpservers.Parse` and `mcpremote.Fields`, both of them; `addable` and its
reason are still on every entry; `GET /api/catalog/{name}` still explains a
refusal in full, because somebody who came looking for one server in particular
is owed the answer; and `?include_unaddable=1` still returns them for an
operator who wants to see what is being withheld. What changed is what a
*listing* is for.

**A limit bounds the page, not each catalogue.** It did not, and the bug was
worth more than it looks. Every source was handed the caller's limit and
honoured it independently, so a request for ten returned thirty and a request
for thirty returned ninety — three sources' worth, merged. The API said one
thing and the endpoint did another, the dashboard rendered and shipped three
times what it asked for, and an operator reading ninety rows concluded the
catalogues held ninety servers between them. They hold something over twelve
thousand.

So `Multi` pages. Each source is asked for a window of twice the page, with a
floor of twenty; the windows are merged in preference order, filtered,
deduplicated, and handed out `limit` at a time. Sources are *read* round-robin
even though duplicates are *resolved* in preference order, because reading in
preference order would mean the second catalogue was never reached until the
first's twenty-four thousand entries ran out.

That forces the cursor to carry more than a cursor. A bounded page very often
stops halfway through a source's window, and a source's own cursor can only say
"the next window" — resuming there would silently drop the other half of every
window in every catalogue, which nothing but a total would reveal. So each
source's position is a pair: its own cursor, and how far into that window the
last page reached. Re-asking for a half-read window is free, because the
per-source cache is in front of it — which is also why the over-fetch pays for
itself. Measured against three catalogues at a 120 ms round trip each, page one
costs the same as it always did (both shapes fan out concurrently and both wait
on the slowest catalogue) and page two went from a 251 ms fan-out to a cache
read, while the default listing's payload fell from 47 KiB to 5.9 KiB.

**How big is it.** A page of ten out of twelve thousand looks exactly like a
catalogue of ten, so the page says roughly how many servers can be added and
the search box sits next to that number rather than above a grid. It is an
estimate and is rendered as one — rounded down to two significant figures and
carrying a `+` — because it cannot honestly be anything else. Only two of the
four sources report how much they hold: Smithery sends a `totalCount`, and
Docker's catalogue arrives as one document whose length is the count. Neither
reports how many of its servers *this host* would accept, and finding out for
certain means parsing twenty-four thousand `server.json` files behind a page
load. So the ratio is measured over the documents that were parsed anyway while
the page was built, and applied to the size the source gave. Smithery's sample
is its most popular five hundred and so runs optimistic; the two sources that
report no size contribute only what was seen and so run far short; a source
that did not answer contributes nothing and the page says so, because a total
that does not move when a catalogue goes down is worse than a smaller one.

**An icon is a URL, and a URL is not a picture.** Smithery, Docker and
`server.json` all offer one, and it goes straight into an `<img src>` on an
administrator's page — so it is allow-listed rather than sanitised: `https`
only, absolute, a real host, no credentials, no control characters, length
bounded, and omitted entirely if it is anything else. `http` is refused because
a dashboard served over TLS should not be making plaintext subresource
requests; `data:` is refused because an SVG carries script and this would put
it in the page's own origin. Nothing here fetches it — no proxying, no
prefetch, no reachability check — because a server-side fetch of an address a
third party chose is a server-side request forgery whatever it is called. The
browser fetches it, lazily, with no referrer, and a dead host costs one
placeholder.

**A composed document is still a document.** Docker's format is not
`server.json`, so an entry is translated into one — the derived name says where
it came from, `${ENV}` in a header becomes a `{placeholder}` with a variable
behind it marked secret, and the result goes to the same import endpoint, is
judged by the same two calls, and is stored verbatim as composed. The
translation is byte-stable, because the import path hashes what it stores.

**Five `server.json` formats are read, not one.** Every dated schema published
to date is vendored beside the current one, and an earlier document is
translated into the internal model explicitly rather than parsed optimistically.
What actually moved between them is small and is listed in `schema.go`:
2025-07-09 spelled an input's flags `is_required` and `is_secret`, and
`remotes[].variables` — the map that says what a `{placeholder}` in a url means
— arrived only with 2025-12-11. The first is read under both spellings and
OR-ed, because that direction can only add protection to a credential and the
other can only remove it. The second is not read where the format does not
define it: an earlier document carrying a url placeholder is refused with its
version named, because substituting from a map the format never had would be
this host inventing a meaning and then dialling the address it produced. A
`$schema` that is none of the five is still refused — the pin is by URI, so the
right date at the wrong address is not that format.

**Nothing about the catalogue is on a startup path.** Every client is
constructed at boot and reaches nothing; the first fetch happens on the first
request that asks for one. A catalogue that cannot be reached serves what it
last saw, marked stale with the time it was fetched. A third party being down
is not this deployment's failure and is not worth a page that will not render —
but neither is it worth pretending the data is current.

**How long an answer is reused is the catalogue's to say.** A single hardcoded
TTL is wrong in both directions at once, and measurably so: the official
registry sends no `Cache-Control` and no validator at all, Docker's CDN sends
an `ETag` and a `Last-Modified` and no policy, and other catalogues send
`no-cache` or grant four hours to a shared cache and a day of
`stale-while-revalidate`. So `Cache-Control` is honoured where it is sent —
`s-maxage` in preference to `max-age`, because mcpd is a shared cache and not
one person's browser — `Age` is deducted, and the configured default stands in
only where a catalogue said nothing. `no-cache` with no validator to revalidate
against becomes a very short life rather than being ignored. A stale answer
inside the window a catalogue granted is served immediately and refreshed
behind it, one refresh per key, owned by the cache and cancelled at shutdown.
A refresh sends `If-None-Match` and `If-Modified-Since` when a validator is
held, which turns re-reading Docker's 567 KiB catalogue into a `304`. One
`server.json` is held longer than a listing, because it is a different question
keyed by a stable name; "no such server" is held for seconds, because a name
that 404s today is a server published tomorrow. `?refresh=1` bypasses all of it
for one request, for the administrator standing in front of a catalogue that is
visibly behind.

**One source's failure is one source's.** Sources are asked concurrently and
the whole fan-out is bounded, so the slowest catalogue does not decide how long
a page takes. What arrived is served, and the response says which catalogues
answered, which were stale, how many entries each contributed, and what went
wrong with the rest — a shorter list that does not name the missing catalogue
reads as "there is nothing else" rather than as "we could not ask". A page is
an error only when nothing answered at all.

**The memory bound is on the process, not on each catalogue.** One store behind
every cache, because a cap each source gets its own copy of is a cap a fourth
source silently quadruples.

That store is `internal/cachestore` and it is not a cache. It is a bounded map
of timed entries plus the rule that six callers asking one question at the same
moment should cost one answer, and it holds no policy at all — because the
policy differs between the two things that use it, and a store that decided it
would be one of them wearing a general name. Everything a catalogue is
particular about stays here: `stale-while-revalidate`, validators, and the
short memory of a name that 404s.

**The registry's content is a third party's text, arriving in whatever quantity
they choose to send.** The response is bounded before it is decoded, the entry
count per page is capped, and every field is bounded and stripped of control
and invisible-formatting characters before it is stored or returned.

**Deduplicate by name within a catalogue, by address across them.** The
official registry holds every version of every server and returns them all
unless asked otherwise; the query asks for `version=latest` and the
deduplication runs anyway, because "the far end promises one row per name" is
exactly the kind of promise whose failure shows up as a page listing the same
server four times.

Across catalogues a name cannot do the job — the official registry calls it
`app.linear/linear` and Docker calls it `linear`, and no rule turns one into the
other. The address does: thirty-two of the entries those two share resolve to
the same URL, and two entries that dial one endpoint are one server however they
are named. An entry with no address falls back to its own catalogue's name,
since nothing can establish that two unreachable entries are the same thing.

Which copy survives is preference order, and with four sources the order needs
a reason rather than a list. It is one idea applied four times — how far the
entry is from the party that operates the server:

1. **the official registry**, where the publisher registered it themselves;
2. **PulseMCP**, an aggregator, but a pass-through one that hands back that
   same first-party document unchanged;
3. **Docker**, whose entry is not a `server.json` at all but a document this
   host composed from a third party's description;
4. **Smithery**, which describes its own proxy in front of the server rather
   than the server.

The pair that actually collides is the first two, because PulseMCP mirrors the
official registry — which is exactly what the order is for. Smithery rarely
competes, and understanding why matters more than the fact: every Smithery
entry is addressed at `server.smithery.ai`, so a Smithery row and an official
row for what is recognisably the same project have different addresses and do
not merge. That is right rather than a miss. Dialling the publisher's endpoint
with the publisher's key and dialling Smithery's gateway with a Smithery key
are two different servers by every test that matters here — different address,
different credential, different party to trust — and merging them on the
strength of a similar name would hide one of two real choices.

Browsing takes `admin`. Everything it returns is public; the privilege is
making this host reach a third party from inside the deployment. Nothing about
it changes state, so nothing about it is audited — importing what it found is,
like any other import.

Every client is behind one interface, and the cache and the multiplexer are
themselves clients over clients: a cache in front of each source, so that one
being down is that source's staleness rather than the page's, and the
multiplexer in front of the caches, so that the handler still talks to a single
catalogue.

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

The container's data directory is bind-mounted at `./data`. It is an ordinary
directory owned by whoever runs the container, so `go build ./...` reads it
like any other and finds no packages in it.

## Deployment

Primary target is a Linux VM with systemd. [`deploy/mcpd.service`](../deploy/mcpd.service)
is a hardened unit: dedicated user, `ProtectSystem=strict`, all capabilities
dropped except `CAP_NET_BIND_SERVICE`, and a syscall filter.

### The container

**One mount, and it is generated.** `./data` holds `config.yaml`, the database,
TLS material and out-of-process plugins. `docker compose up` against an empty
directory produces a working host: the entrypoint runs `mcpd -init` when there
is no config, which writes the file and generates a bearer token and the key
that encrypts stored credentials.

**Generation happens exactly once, and that is the load-bearing part.**
`secret_key_ref: env:MCPD_SECRET_KEY` is what encrypts every credential typed
into the dashboard, so a restart that generated a second key would make every
one of them undecryptable — and `Store.Get` drops what it cannot decrypt, so
nothing would say so beyond a credential quietly no longer being there. Three
things hold the line. `mcpd -init` refuses outright to overwrite an existing
`config.yaml` or `.env`; it declines to write a key at all when the environment
already supplies one, rather than writing a second that would take over the day
the environment stopped; and the entrypoint calls it only when neither file
exists, refusing rather than generating a config beside secrets it did not
write.

**Alpine, not distroless, and the trade is stated rather than waved away.**
What is lost is real: a shell in the image gives a remote code execution more
to work with, and there is a musl userland and a package manager to keep
patched. What is not lost is what actually hardens the container — read-only
root filesystem, `cap_drop: ALL`, `no-new-privileges`, a nonroot user, `/tmp`
on tmpfs, and a static CGO-free binary. What is bought is the two things
distroless made impossible, which were the whole of the complaint: nothing can
run before the binary, so the config had to be hand-authored and bind-mounted,
and the volume had to be pre-chowned to uid 65532 — which left an operator
with a data directory their own account could not read.

**The container runs as the host user's uid**, `${UID:-1000}:${GID:-1000}`,
rather than chowning the mount from an entrypoint. Chowning a bind mount needs
the container to start as root holding `CAP_CHOWN`, `CAP_SETUID` and
`CAP_SETGID`, which means handing back three of the capabilities dropped above
to solve a problem that has a solution needing none of them.

**`config.yaml` is no longer mounted read-only**, because it lives in the one
writable mount. That is a real change and worth naming: what kept mcpd from
rewriting an operator's YAML was never only the mount flag — there is no code
that writes it, and there is no reason to add one (see *plugin overrides*
below). Under systemd it is still under `ProtectSystem=strict`.

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

**A rate limit refuses; it does not queue.** It used to wait for a turn, which
looks like the polite thing to do and is the wrong thing here. The caller is a
model with a deadline: a queued call arrives at the front having spent most of
the budget it needed to do the work, and every caller behind it holds a
goroutine and a context for as long as the queue is. Refusing immediately turns
a hidden stall into a fact the model can act on, so the error says how long to
wait and in what units. A refusal does not consume the turn it was refused,
which is what keeps a burst of rejections from pushing everybody back.

**A read tool's result is evidence a model acts on, which is what decides
whether it may be reused.** Caching a plugin read is not only a freshness
question. A stale device state does not merely look out of date to a person; it
is a premise a model reasons from and then proposes a change against. So where
a plugin caches, three rules hold, and they are the opposite of the
catalogue's.

Nothing stale is ever served. The catalogue cache serves an expired answer
while a refresh runs behind it, because a browse page rendering slightly behind
beats one that does not render. Here the reader is about to act, and "this is
what the estate looked like a while ago" is not a safer answer than waiting.

What may be reused at all is an allow-list, so an endpoint nobody has thought
about is fetched every time. And a key is built from the upstream request that
will actually be made — the endpoint and the fully resolved query — never from
the arguments a tool was called with. That is what makes a shared cache
defensible: every caller of one plugin instance reaches the same upstream with
the same credential, so two callers producing one key produce byte-identical
requests and therefore identical responses. A plugin whose request varies by
caller — a per-user token, a header derived from the principal — must put the
caller in the key or not cache at all. A cache keyed without the caller, where
the response depends on them, is an access-control hole rather than a
performance decision.

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
instance knows which it came from, because the two are removed differently.

**The dashboard can remove a file-declared plugin, and mcpd never touches the
file.** There is no code anywhere that writes `config.yaml`, and under systemd
`ProtectSystem=strict` would refuse it anyway. The container used to enforce it
too, by mounting the file read-only; it now lives in the writable data
directory, so the guarantee there rests on the absence of the code rather than
on the mount. Nor should it — rewriting hand-authored YAML destroys
comments, ordering and anchors, and in any deployment provisioned by
configuration management the next deploy would put the entry back. "Remove it
from the file instead" was therefore an instruction that a great many operators
could not carry out, which made it a dead end rather than an answer.

So a removal is a row in `plugin_overrides` saying the file's declaration for
that name is ignored, and every read of the instance list applies it. It
survives a restart because it is in the database and the file is not consulted
about it; it beats a redeploy for the same reason. The same row carries an
override of `enabled`, which is the identical dead end one step smaller.

**Keyed on the name, not on the declaration.** Pinning the override to a hash
of the file entry — the way `descriptor_hash` pins a tool approval — would mean
that editing the entry silently resurrects the plugin, which is the failure
this exists to prevent. A tool approval is a statement about a descriptor; a
removal is a statement about a name.

**Reversible, and reversible to the file.** Restoring forgets the override
entirely, so what comes back is whatever the file declares now rather than a
copy of what it declared then. The settings are kept across a removal, because
a restore that came back without the credentials somebody typed in would be a
restore in name only — which is also the difference from removing a
dashboard-defined instance, where the settings do go, so that a name reused
later cannot silently inherit them.

**Removing one is an administrative act and is audited.** It overrides the
deployment's own configuration, so it appends to the hash-chained trail inside
the transaction that performed it, like importing a server or classifying a
tool, rather than landing in the settings history. `required: true` is the
deployment saying the host should not run without an integration; removing one
of those is allowed and takes an explicit acknowledgement, because it should
not be a side effect of confirming something else. Every override is named in
the log at startup for the same reason `shadowedNames` is: a plugin the file
says is enabled and that this host is not serving is hard to diagnose from
outside.

**A removal outlives the declaration it overrode.** An operator who removes a
plugin here and later deletes the entry from their YAML leaves a row matching
nothing. Those are kept rather than discarded — one start against a truncated
or missing file would otherwise forget every removal and resurrect all of them
on the next good deploy — and are reported to the dashboard so they can be
forgotten deliberately.

Adding an instance records intent; it does not mount. A plugin is built once,
at startup, from the settings it had then, so the dashboard says a restart is
needed rather than showing an instance whose tools never appear.
