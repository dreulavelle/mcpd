# Working on mcpd

Read [`docs/architecture.md`](docs/architecture.md) first. It explains the
shape and the decisions; this file is only the things that are easy to get
wrong.

## Build

```bash
make check   # fmt, vet, test, dependency pinning — run before calling work done
make race    # tests under the race detector
make web     # rebuild the dashboard bundle after any change under web/
```

The dashboard is embedded with `go:embed` from `internal/admin/dist`. Editing
`web/src` changes nothing the binary serves until the bundle is rebuilt and
copied there.

The container's data lives in `./.data`. The leading dot keeps `go build ./...`
working — `cmd/go` skips dot-prefixed directories, and the TLS material inside
is mode 700 owned by the container user.

## Rules that are load-bearing

**Migrations are forward-only and checksummed.** Never edit one that has run;
add the next number. There is no down path.

**Every state change is a guarded SQL statement.** Conditions belong in the
`WHERE` clause, not in Go before the write. If a claim can be lost to a race,
it should match zero rows and say so.

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
carries a human's yes and nothing else. Do not let the second wear the first's
name.

**Indeterminate is not terminal.** It means a write may have landed. Treating
it as failed invites a retry that applies the change twice.

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
