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
cp .env.example .env
$EDITOR .env
docker compose up -d
```

The dashboard is on **port 80**; the MCP endpoint is on loopback:8080.

Open the dashboard and it asks you to create the first account. That account is
the administrator, and registration stops being offered once it exists.

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

Scripts that cannot fill in a sign-in form use a bearer token from
`auth.static_tokens` instead.

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
`approval.inline_max_risk` sets how consequential a change may be before it has
to be shown in full and approved explicitly instead.

Either way the record is the same. mcpd will not execute a change without an
approval stored in its database, so an assistant has no path that skips it.

## Configuration

See [`configs/example.yaml`](configs/example.yaml).

**Secrets are referenced, not stored.** Every credential names an `env:`,
`credential:` (systemd `LoadCredential`), or `file:` reference resolved at
startup. A config file that never holds a token cannot leak one.

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
image is 24 MB, distroless, non-root, with a read-only root filesystem.

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
