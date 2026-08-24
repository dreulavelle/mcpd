# mcpd

An extensible host for Model Context Protocol integrations. One binary serves
many plugins, each on its own endpoint, with every infrastructure change gated
behind an approval it can prove.

```
                         ChatGPT
                            │  Secure MCP Tunnel (outbound)
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
        /mcp/echo      /mcp/…        /mcp/…
              └─────────────┼─────────────┘
                            ▼
                          mcpd
                     (single binary)
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
          plugins        SQLite      dashboard
```

Nothing has to be publicly reachable. The tunnel dials outward and holds the
connection open, so ChatGPT reaches mcpd with no inbound port, no public DNS,
and no NAT rule.

## Quick start

### Docker

```bash
docker compose up -d
```

That is the whole of it. There is nothing to fill in first: `./data` starts
empty, and the container generates a `config.yaml`, a bearer token and the key
that encrypts stored credentials into it on first start. Everything a
deployment owns is in that one directory — the configuration, the database, TLS
material and out-of-process plugins — and it is owned by your own account, so
you can read and edit it without `sudo`.

The dashboard is on **port 80**; the MCP endpoint is on loopback:8080. Open the
dashboard and it asks you to create the first account. That account is the
administrator, and registration stops being offered once it exists.

Copy `.env.example` to `.env` to change the published ports. If your account is
not uid 1000, put your own in it, or `./data` comes back owned by somebody
else:

```bash
printf 'UID=%s\nGID=%s\n' "$(id -u)" "$(id -g)" >> .env
```

**Keep `data/.env`.** It holds `MCPD_SECRET_KEY`, and every credential saved
from the dashboard is encrypted with it. Lose it and those credentials cannot
be read back. mcpd will not replace it: `-init` refuses to overwrite an
existing config or `.env`, and the container's entrypoint only generates when
neither is there.

#### Moving an existing deployment

Before this, the container kept its data in `./.data`, its config in
`./config.yaml`, and its secrets in `./.env`. All three move into `./data`, and
nothing is regenerated — the point of doing it by hand is that the existing
`MCPD_SECRET_KEY` comes with you.

```bash
docker compose down

# .data was owned by uid 65532, which is the thing this release fixes. This is
# the last time you need sudo for it.
sudo chown -R "$(id -u):$(id -g)" .data

mkdir -p data/plugins
cp -a .data/.   data/               # database, TLS material, plugin binaries
cp -a config.yaml data/config.yaml  # storage paths inside it are container
                                    # paths and do not change
cp -a .env      data/.env           # keeps the existing MCPD_SECRET_KEY

# The root .env is now only the published ports, which is what compose reads.
printf 'MCPD_PORT=8080\nMCPD_BIND=127.0.0.1\nMCPD_FRONTEND_PORT=80\nMCPD_FRONTEND_BIND=127.0.0.1\n' > .env
printf 'UID=%s\nGID=%s\n' "$(id -u)" "$(id -g)" >> .env

docker compose up -d --build
```

Nothing is deleted, so the old layout is still there if this goes wrong. Check
`docker compose logs` for `database ready`, then open a plugin's settings: if a
credential you saved is still there, the key came across. Once it has,
`rm -rf .data config.yaml` — and note that `mv .data data` is not the move to
make, because `./data` already exists and you would end up with `data/.data`.

### From source

```bash
make build
./bin/mcpd -config configs/example.yaml
```

Check a config without starting anything:

```bash
./bin/mcpd -config /etc/mcpd/config.yaml -check
```

## Connecting ChatGPT

Create a tunnel on the **Tunnels** page, then add it as a connector in ChatGPT
by its tunnel id. Nothing needs to be exposed: the tunnel authenticates itself
to OpenAI with a runtime API key, and mcpd stays private.

One tunnel carries one address, so it is one connector. Give a plugin its own
tunnel and that connector reaches that system and cannot discover any other;
give the host one tunnel and it reaches everything the tunnel's identity is
granted. What the connector may do — a display name, a role, the plugins it may
reach — is set alongside it.

## Signing in

People sign in with their own email and password. Administrators manage
accounts on the **Users** page.

|            | reads | proposes changes | approves them | changes settings |
|------------|:-----:|:----------------:|:-------------:|:----------------:|
| **User**   |   ✓   |        ✓         |       ✓       |                  |
| **Admin**  |   ✓   |        ✓         |       ✓       |        ✓         |

A user sees the settings and how each plugin is reached; they just cannot
change either. Admins additionally make and assign tunnels, manage accounts,
and clear history.

A role says what somebody may *do*. What they may *reach* is a separate
question, answered by **Groups**: a group lists systems, and everyone in it can
reach them. A new group lists none, and an account in no group reaches nothing
until somebody grants it something.

Scripts and agents that cannot fill in a sign-in form present a bearer token.
Two kinds work, and they are the same thing in two places: one declared in
`auth.static_tokens` in the configuration file, and one an administrator
creates on the **Keys** page. A key made there can be revoked or re-scoped
without restarting mcpd, can expire, and shows when it was last used — and it
acts as itself, so the history says which agent did what. Its secret is shown
once, when it is created.

## How a change gets made

```
  echo_set_label                   →  operation_id, state=pending_approval
       (nothing has changed)

  echo_approve_operation           →  state=approved
       (a human decides)

  executor                         →  reload, revalidate, claim, apply, verify
       (at most once)              →  state=succeeded, verified=true
```

Between approval and execution mcpd re-plans against live upstream state and
compares preconditions. If the target changed after approval, the change is
refused rather than applied over someone else's work.

Most of the time you will not see those as two steps. When the assistant can
ask you directly it does, and confirming in the conversation is the approval.
"Approve in the conversation up to", on the Settings page, sets how
consequential a change may be before it has to be shown in full and approved
explicitly instead.

Either way the record is the same. mcpd will not execute a change without an
approval stored in its database, so an assistant has no path that skips it.

## Configuration

Almost all of it is in the dashboard, and the config file is five lines. A
generated one looks like this in full:

```yaml
server:
  listen: "127.0.0.1:9080"
  frontend_listen: "127.0.0.1:9090"

storage:
  path: /var/lib/mcpd/mcpd.db

secret_key_ref: env:MCPD_SECRET_KEY
```

Those four cannot live in the database. `storage.path` is where the database
is, and it cannot say where it is from inside itself. `secret_key_ref` names
the key everything secret in the database is encrypted under, and a lock does
not hold its own key. The two bind addresses are a judgement rather than a
limit: a bad one stored in the database would lock you out with no page left to
fix it on, so the file is the way back in.

Everything else — the address to advertise, TLS, timeouts, logging, approval
policy, sessions, ChatGPT — is a setting on the Settings page. That is not
tidiness. Editing a file leaves no record of who changed what, or what it was
before; a settings change records both, and the dashboard can show you.

Upgrading from a version that kept these in the file needs no hand-editing.
The first start imports what your file sets, once, records it against
`system:config-import`, and ignores those keys from then on. Any that are left
behind and disagree with what mcpd is running get named at startup, so the two
never quietly differ. See [`configs/example.yaml`](configs/example.yaml) for
what a fuller file can still declare.

### Where the secrets are

There are no plaintext passwords on disk, and there never were. Account
passwords are bcrypt in the database. Plugin credentials, SSO client secrets
and the tunnel's API key are encrypted at rest. API keys are stored as digests
— mcpd cannot read one back, only check one. Static tokens in the file are
*references* (`env:`, `credential:` for systemd `LoadCredential`, or `file:`),
resolved at startup, so a config file that never holds a token cannot leak one.

The one secret in the clear is `MCPD_SECRET_KEY`, in `data/.env` at mode 600.
It is the key everything above is encrypted under, so it is the one thing that
cannot itself be encrypted by the system it unlocks. Guard that file; a stolen
copy of the database without it is unreadable.

**Scoping is per credential.** This agent reaches `/mcp/echo` and nothing else
— every other endpoint returns 404, so it cannot discover what else is
deployed:

```yaml
auth:
  static_tokens:
    - id: chatgpt-echo
      secret_ref: env:MCPD_TOKEN_CHATGPT
      principal: svc:chatgpt
      role: user
      plugins: [echo]
```

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

mcpd mounts it at `/mcp/weather`. See [`examples/echo`](examples/echo) for a
complete plugin including an approval-gated mutation, and the [`sdk`](sdk)
package docs for the mutation contract.

The rule that matters most: if a mutation's `Apply` cannot establish whether
its write landed, it must return `sdk.Indeterminate`. Anything else tells the
host the write did not happen, and permits a retry that applies it twice.

## Deployment

Primary target is a Linux VM with systemd;
[`deploy/mcpd.service`](deploy/mcpd.service) is a hardened unit. The container
image is Alpine, non-root, with a read-only root filesystem, every capability
dropped and `no-new-privileges` set. The shell is what lets it generate a
config and run as your own uid; the hardening is what makes that a fair
trade, and none of it is given up.

Terminate TLS at a reverse proxy and bind mcpd to loopback, or let mcpd issue
its own certificate with `server.tls.mode: self-signed`.

## Managing it

Everything an operator sets lives in the dashboard: accounts, tunnels,
approval policy, and each plugin's own configuration. A plugin declares the
settings it needs and the dashboard renders them, validates what is typed, and
stores secrets encrypted — so credentials never go in a file or an environment
variable.

The configuration file still works, and is the right place for provisioning a
host nobody has opened yet. Anything set there is a starting value the
dashboard can change, and a secret in it is a reference rather than a value.

## Integrations

| Plugin | What it manages | State |
|---|---|---|
| `echo` | Nothing real — a worked example, including an approval-gated write | Bundled |
| `cnmaestro` | Cambium cnMaestro: Wi-Fi and fixed-wireless estates | Read-only |
| `observium` | Observium: SNMP monitoring — devices, interfaces, sensors, capacity, topology, alerts | Read-only |

A remote MCP server somebody else runs is the third kind. Paste its
`server.json`, or find it in the dashboard's catalogue of the official MCP
registry — either way it is the same import, and nothing it offers is served
until an administrator has read each tool and enabled it. Servers published
only as something to run locally are listed but cannot be added; mcpd connects
to remote servers and does not execute packages.

Anything else is a plugin you write. See below.

## Status

Working: the MCP host with per-plugin endpoints and scoping; OpenAI's Secure
MCP Tunnel, embedded, one per connector; accounts with first-run registration;
the approval engine end to end, including confirmation raised in the
conversation; SQLite storage with a hash-chained audit trail; the dashboard;
and the out-of-process plugin SDK.

## Development

[`docs/architecture.md`](docs/architecture.md) covers how it is put together —
packages, the state machine, the storage model, and the build.
