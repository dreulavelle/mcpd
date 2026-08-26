# mcpd

Let ChatGPT read and manage your infrastructure, without opening a port and
without letting it change anything nobody approved.

mcpd runs on your network and serves your systems to an AI assistant over the
Model Context Protocol. Ask it *"which switches are down?"* or *"why is the
Porter school's uplink flapping?"* and it answers from your live monitoring —
because it is talking to your monitoring, not guessing.

```
                         ChatGPT
                            │  Secure MCP Tunnel (outbound)
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
        /mcp/observium  /mcp/graylog  /mcp/…
              └─────────────┼─────────────┘
                            ▼
                          mcpd
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
          plugins        SQLite      dashboard
```

**Nothing is exposed to the internet.** The tunnel dials outward and holds the
connection open, so there is no inbound port, no public DNS and no NAT rule.

**Reads are free; changes are not.** An assistant can look at anything it has
been granted. To *change* something it must propose the change, and mcpd will
not execute it without an approval stored in its database — so there is no path
that skips you. Small changes you confirm in the conversation; consequential
ones wait in a queue.

**Every connector sees only what you gave it.** A connector scoped to your
monitoring cannot discover that your Wi-Fi controller is also here.

## Install

### Docker

```bash
git clone https://github.com/dreulavelle/mcpd.git && cd mcpd
docker compose up -d
```

That is the whole of it. `./data` starts empty and the container generates its
configuration, a bearer token and an encryption key on first start.

The dashboard is on **port 80**. Open it and it asks you to create the first
account — that account is the administrator, and registration closes once it
exists.

Copy `.env.example` to `.env` to change the published ports. If your account is
not uid 1000, add your own or `./data` comes back owned by somebody else:

```bash
printf 'UID=%s\nGID=%s\n' "$(id -u)" "$(id -g)" >> .env
```

### Debian

```bash
arch=$(dpkg --print-architecture)
repo=https://github.com/dreulavelle/mcpd
# The package filename carries its version, so ask which one is current.
ver=$(curl -fsSL https://api.github.com/repos/dreulavelle/mcpd/releases/latest \
      | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')

curl -fsSLO "$repo/releases/download/v${ver}/mcpd_${ver}_${arch}.deb"
sudo apt install "./mcpd_${ver}_${arch}.deb"
```

The package makes the service account, generates the configuration, installs
the systemd unit and starts it. The dashboard is on port 80.

There is nothing else to install — the dashboard and the database are compiled
into the binary, so there is no runtime and no database server to keep in step.
Prefer a plain binary? Every release also ships `mcpd-linux-amd64` and
`mcpd-linux-arm64` with a `SHA256SUMS`.

> **Keep `/var/lib/mcpd/.env`** (or `data/.env` under Docker). It holds the key
> every stored credential is encrypted with. Lose it and those credentials
> cannot be read back. Back up that directory and you have backed up the
> deployment.

## Your first connector

One tunnel is one connector in ChatGPT.

1. **Add the system you want to reach.** On **Plugins**, add an instance of a
   built-in integration and fill in its address and credentials. They are
   validated when you save and stored encrypted.
2. **Create a tunnel.** On **Tunnels**, create one and choose which plugins it
   may reach. You will need an OpenAI *runtime* API key — an admin key cannot
   run a tunnel.
3. **Add it in ChatGPT.** Settings → Connectors → add by tunnel id.
4. **Ask it something.** *"List the devices that are down."*

Give a plugin its own tunnel and that connector reaches that system and nothing
else. Give the host one tunnel and it reaches everything that tunnel's identity
is granted.

## Who can do what

People sign in with their own email and password; administrators manage
accounts on the **Users** page.

|           | reads | proposes changes | approves them | changes settings |
|-----------|:-----:|:----------------:|:-------------:|:----------------:|
| **User**  |   ✓   |        ✓         |       ✓       |                  |
| **Admin** |   ✓   |        ✓         |       ✓       |        ✓         |

A role says what somebody may *do*. What they may *reach* is a separate
question, answered by **Groups**: a group lists systems, and everyone in it can
reach them. An account in no group reaches nothing until somebody grants it
something.

Scripts and agents that cannot fill in a sign-in form use an API key from the
**Keys** page. Each acts as itself, so the history says which agent did what.

## Keeping it running

**System** shows the running version, any releases published since it with
their notes, what the host is using in memory and CPU, and a restart button.

Update checking is off until you turn it on under Settings → Updates — mcpd
sits inside your network, and reaching out to github.com on a timer is a
connection worth agreeing to rather than discovering.

See [Upgrading](docs/upgrading.md) for how to apply one.

## What it can manage

| Plugin | What it manages | State |
|---|---|---|
| `graylog` | Graylog: log search, aggregations, alerts and alert rules, and the installation's own health | Read-only |
| `observium` | Observium: devices, interfaces, sensors, capacity, alerts. Needs the subscription REST API | Read-only |
| `cnmaestro` | Cambium cnMaestro: Wi-Fi and fixed-wireless estates | Read-only |
| `echo` | Nothing real — a worked example, including an approval-gated write | Bundled |

You can also add any remote MCP server somebody else runs: paste its
`server.json` or find it in the dashboard's catalogue. Nothing it offers is
served until an administrator has read each tool and enabled it.

Anything else is a plugin you write — see [Writing a plugin](docs/plugins.md).

## Documentation

| | |
|---|---|
| [Configuration](docs/configuration.md) | The five-line config file, and where the secrets are |
| [How a change gets made](docs/approvals.md) | The approval path, end to end |
| [Approval policy](docs/approval-policy.md) | Rules that let routine changes through |
| [Upgrading](docs/upgrading.md) | Applying an update, and moving an older deployment |
| [Writing a plugin](docs/plugins.md) | The plugin SDK |
| [Architecture](docs/architecture.md) | How it is put together |

## Deployment notes

The container is Alpine, non-root, with a read-only root filesystem, every
capability dropped and `no-new-privileges` set.
[`deploy/mcpd.service`](deploy/mcpd.service) is a hardened systemd unit for
running it directly.

Terminate TLS at a reverse proxy and bind mcpd to loopback, or let it issue its
own certificate with `server.tls.mode: self-signed`.

## Status

Working: the MCP host with per-plugin endpoints and scoping; OpenAI's Secure
MCP Tunnel, embedded, one per connector; accounts with first-run registration;
the approval engine end to end; SQLite storage with a hash-chained audit trail;
the dashboard; and the out-of-process plugin SDK.
