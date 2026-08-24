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
map changes, this one has to follow.

The container's data lives in `./data` — one bind mount holding `config.yaml`,
the database, TLS material and out-of-process plugins. It is generated on
first start if it is empty, and the container runs as the host user's uid so
what lands there is yours to read and edit.

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

**A tunnel carries its own identity**, so it builds its own MCP server rather
than sharing the cached one. Writing a principal into a shared server lets the
first caller answer for everyone.

## Conventions

Comments explain *why*, and are worth writing where a reader would otherwise
wonder. Do not narrate what the code plainly does.

Tests state the behaviour they defend, including the bug they exist for when
there was one. Table-driven where it fits.

Conventional commits, branch names `type/short-description`. Commit bodies
explain the reasoning, not the diff.

`gofmt`, `go vet`, and the tests must pass before Go work is finished. Prefer
the standard library; every dependency is a thing to keep in step.
