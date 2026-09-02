# Working on mcpd

Read [`docs/architecture.md`](docs/architecture.md) first. It explains the
shape and the decisions; this file is only the things that are easy to get
wrong.

## Build

```bash
make check     # fmt, vet, test, dependency pinning — run before calling work done
make race      # tests under the race detector
make web       # rebuild the dashboard bundle after any change under web/
make web-test  # the dashboard's own tests (vitest)
```

The dashboard is embedded with `go:embed` from `internal/admin/dist`. Editing
`web/src` changes nothing the binary serves until the bundle is rebuilt and
copied there.

The dashboard is React with Tailwind and shadcn/ui. shadcn components are
*copied in* under `web/src/components/ui`, so they are ordinary source: edit
them rather than working around them. `web/src/lib/nav.ts` is the one place
that decides what appears in the sidebar, and `web/src/lib/capabilities.ts`
mirrors the role-to-capability map from `internal/auth/principal.go` — if that
map changes, this one has to follow. That mirror describes what a *role*
carries; it is not what decides whether a control is drawn. A group can take
capabilities away, so the session reports the effective set (`capabilities`
on `GET /api/session`, computed by `Principal.Capabilities`) and `useCan`
reads that. Deriving it from the role showed a restricted administrator
every button and refused every click.

The container's data lives in `./data` — one bind mount holding `config.yaml`,
the database, TLS material, the rotating log in `logs/` and out-of-process
plugins. It is generated on first start if it is empty, and the container runs
as the host user's uid so what lands there is yours to read and edit.

It used to be `./.data`, because a distroless image forced the volume to be
owned by uid 65532 and a directory the host user could not read broke
`go build ./...`. That was a workaround for the ownership problem rather than a
requirement of its own; the ownership is fixed, so the dot is gone. Nothing
inside `./data` is a Go package, and `make fmt` walks `go list` rather than the
tree, so neither notices it.

## Rules that are load-bearing

**Migrations are forward-only and checksummed.** Never edit one that has run;
add the next number. There is no down path.

**Every state change is a guarded SQL statement.** Conditions belong in the
`WHERE` clause, not in Go before the write. If a claim can be lost to a race,
it should match zero rows and say so.

**Configuration has one authority per setting.** Four keys live in
`config.yaml` — `storage.path`, `secret_key_ref`, and the two bind addresses —
and every other setting lives in the database. The file is not consulted for a
moved key; it seeds the store once on the first start after an upgrade, and a
key left behind that disagrees is named in a startup warning. Silent
disagreement between two sources is the failure mode this exists to remove.

The exception is a *collection*, and it is an exception to the shape rather
than to the rule. `settings` is a flat key/value store, so several ChatGPT
accounts there would mean synthesising `tunnel.account.3.api_key` — a table
with the constraints left out. They live in `chatgpt_accounts`, where a name
can be unique and a credential can be NOT NULL. Each is still the only
authority for what it holds; what stays in `settings` is the *assignment* of a
tunnel to an account, beside the tunnel id it already held, because those are
one decision made on one page.

**Capabilities, not roles.** Check `principal.Can(auth.CapAdmin)`, never
`role == "admin"`. Roles are `user` and `admin`; the map from roles to
capabilities is the only place that knows the difference.

**A claim of verification is earned, never assumed.** A mutation declares
`Verifiable`; when it is false the executor performs no check and settles
`outcome_verified` null. Null is "not checked", `false` is "checked and did not
match", and they must not be collapsed. Likewise, two absent precondition
snapshots comparing equal is not a drift check that passed — it is one that
never ran, and `CheckDrift` says which.

**"Reviewed change" and "gated call" are different words on purpose.** The
first carries exact fields, drift detection and a confirmed outcome. The second
carries an authorisation and nothing else. Do not let the second wear the
first's name.

**Assurance says what can be proved, not who authorised it.** A standing rule
can approve a change without anyone being asked, so a gated call no longer
implies a human said yes. `AuthorizedByRule` is the separate fact. Do not fold
the two together: an auto-approved change can carry every proof, and one a
person approved can carry none.

**Indeterminate is not terminal.** It means a write may have landed. Treating
it as failed invites a retry that applies the change twice.

**Approval happens where the work is.** No path sends a person to the
dashboard to approve a tool call. Below the inline ceiling a client's own
confirmation settles it; above it the assistant shows the change in full and
is told explicitly — still in the conversation. The dashboard is for history,
standing rules and the audit trail. An approval that costs a context switch is
one people arrange not to need, and the arrangement they reach for is a rule
broader than the one they meant to write.

**Tool annotations are the only lever over a client's confirmation, so they
must be accurate about what a call *can* do** — not what it usually does.
`destructiveHint` follows `MutationSpec.Reversible`; a mutation sets
`openWorldHint`. They enforce nothing, which is exactly why getting them wrong
is invisible until a change reaches live infrastructure without anyone seeing
it. A standing rule means a propose call can execute before it returns, so a
hint chosen for the case where a human is always asked is wrong in the case
where nobody is.

**Log with the context when you have one.** `ErrorContext(ctx, ...)` rather
than `Error(...)`, because slog hands a handler `context.Background()` for the
plain form and no handler can recover a correlation ID that was never passed.
That ID is what the caller was given in a header and an error body, and it is
the only thing somebody on a machine you cannot reach can quote back. Debug is
for what a support call turns on: what was asked, what was decided, what the
upstream said — never a response body or a query's arguments.

**Nothing leaves the customer's machine that they did not ask to send.** mcpd
runs on somebody else's hardware. Crash reporting is off until a DSN is set,
the DSN is a setting rather than a constant, and everything is scrubbed at one
gate in `BeforeSend` rather than at call sites. A stack trace carries no
argument values and is safe; the error sentences name upstreams and hosts, so
they are a separate opt-in. If you add a field to a report, scrub it there.

**A tunnel carries its own identity**, so it builds its own MCP server rather
than sharing the cached one. Writing a principal into a shared server lets the
first caller answer for everyone.

That identity belongs to a **ChatGPT account**, not to the host. Several
workspaces can share one mcpd, and when they do the questions worth asking are
per workspace: whose key is this connector using, what may that workspace
reach, and which of them made the call somebody is now reading about. An
account's grant and a tunnel's own grant meet in `bindAccount`, and the
narrower wins — assigning a tunnel to an account can only ever reduce what it
reaches, never widen it. A tunnel with no account does not start: falling back
to some other account's key would have a connector quietly authenticate as the
wrong workspace, which is worse than one that does not come up.

**Tools are named `verb_resource`.** The host prefixes the instance name, so
`search` reaches a model as `graylog_search` — a service and a verb, saying
nothing about what is searched, and unambiguous only until the plugin gains a
second searchable thing. `search_messages` and `search_events` put the answer
in the name. Verbs are a small set — `list_`, `search_`, `get_`, or a domain
verb where none of those is honest — because the verb carries meaning a model
reads before it reads a description. A bare noun (`observium_indicators`) is
the worse form: it names a category somebody invented and no action at all.
Enforced at registration by `plugins.checkToolName`, against the closed set in
`toolVerbs`. Mutations are deliberately outside it: `MutationSpec.Action` stays
`resource.verb` because the approval policy reads it before a model does, and
reordering those words would silently stop a stored exclusion matching.
[`docs/plugins.md`](docs/plugins.md) is the reference.

## Conventions

Comments explain *why*, and are worth writing where a reader would otherwise
wonder. Do not narrate what the code plainly does.

Tests state the behaviour they defend, including the bug they exist for when
there was one. Table-driven where it fits.

Conventional commits, branch names `type/short-description`. Commit bodies
explain the reasoning, not the diff.

**A merge commit must not repeat the branch's subject.** release-please reads
every commit in the range, so a `--no-ff` merge whose subject is the same
conventional commit as the change underneath it puts that change in the
changelog twice. Either squash, or give the merge a plain subject
(`Merge branch 'feat/thing'`) that release-please ignores. 0.7.0 shipped with
every entry duplicated for exactly this reason and had to be corrected by hand
before it was tagged.

`gofmt`, `go vet`, and the tests must pass before Go work is finished. Prefer
the standard library; every dependency is a thing to keep in step.
