# mcpd

### Give ChatGPT access to your private infrastructure — without making it public.

**mcpd** is a self-hosted MCP gateway that securely connects ChatGPT to the apps, services, monitoring systems, and infrastructure running inside your network.

No public IP. No port forwarding. No public FQDN. No exposing internal services to the internet.

```text
ChatGPT  →  Secure outbound tunnel  →  mcpd  →  Your private services
```

Run mcpd next to your infrastructure, connect the systems you want ChatGPT to reach, and start asking questions about your real environment.

*"Which access points are down at the high school?"*
*"What changed on the network before the outage started?"*
*"Why is this laptop dropping off the Wi-Fi?"*

## Features

* **Built-in ChatGPT tunnels** — connect ChatGPT securely without exposing mcpd or your services publicly.
* **Outbound-only connectivity** — no inbound firewall rules, NAT changes, or public DNS required.
* **Multiple ChatGPT accounts** — connect several workspaces at once, each with its own identity and its own access.
* **One gateway, many systems** — combine integrations behind a single endpoint, or give each one its own isolated connector.
* **Multiple instances per integration** — connect as many environments, customers, sites, or servers as you need, independently.
* **Approval-gated actions** — let AI look at anything you allow, while every change waits for a human.
* **Approval policies** — pre-approve the routine work and keep the consequential decisions on your desk.
* **Fine-grained access** — control exactly which users, groups, API keys, and tunnels can reach each system.
* **Encrypted credentials** — integration secrets are stored locally and encrypted. Nothing leaves your network that you didn't send.
* **Tamper-evident audit history** — a hash-chained record of what was asked for, who approved it, and what actually happened.
* **Built-in integrations** — ships ready to connect to real infrastructure, out of the box.
* **Plugin architecture** — add new integrations with the included Go SDK, without rebuilding the platform.
* **MCP marketplace** — discover and import thousands of MCP servers from multiple catalogs.
* **Remote MCP support** — bring MCP servers you already run under one managed, access-controlled gateway.
* **Built-in dashboard** — manage plugins, tunnels, users, permissions, approvals, logs, and system health from your browser.
* **SSO and local accounts** — sign in with Google, Microsoft Entra, GitHub, or any OIDC provider, alongside local users.
* **API keys for automation** — give scripts and agents their own identities and permissions.
* **Self-hosted and lightweight** — one binary with the dashboard, database, MCP host, and tunnel management built in. No runtime to install, no database server to maintain.
* **Docker or bare metal** — deploy wherever your infrastructure already lives.

## Built-in integrations

mcpd currently ships with:

* **Graylog** — search logs, investigate events, alerts, streams, and system health.
* **Observium** — inspect devices, interfaces, sensors, alerts, capacity, and network health.
* **Cambium cnMaestro** — query wireless networks, devices, clients, alarms, topology, and statistics.
* **Extreme Networks ExtremeCloud IQ** — access points, switches, connected clients, alerts, sites, and what's going wrong with any of them.
* **Echo** — a reference plugin demonstrating the plugin and approval system.

The infrastructure integrations are read-only — they look, they don't touch. Need something else? Build your own plugin with the included Go SDK, connect a remote MCP server, or find one in the marketplace.

## Why mcpd?

Most infrastructure is private for a reason.

Your monitoring servers, controllers, internal APIs, management tools, NAS devices, hypervisors, and other services often live behind firewalls with no public endpoint — exactly where they should be.

mcpd gives ChatGPT a secure path **into that environment without turning the environment itself into a public service.**

You decide what ChatGPT can reach.

You decide what it can change.

Your infrastructure stays private.

## Install

### Docker

```bash
git clone https://github.com/dreulavelle/mcpd.git
cd mcpd
docker compose up -d
```

The first start builds the image and takes a few minutes. After that, open:

```text
http://<server-ip>/
```

Create your administrator account and start adding integrations.

> If your user account isn't uid 1000, tell Compose who you are first, or the
> `./data` directory comes back owned by somebody else:
> ```bash
> printf 'UID=%s\nGID=%s\n' "$(id -u)" "$(id -g)" >> .env
> ```

### Debian

Download the latest `.deb` from the [Releases](https://github.com/dreulavelle/mcpd/releases) page and install it:

```bash
sudo apt install ./mcpd_<version>_<arch>.deb
```

mcpd starts automatically as a system service. Open:

```text
http://<server-ip>/
```

That's it. Plain `linux-amd64` and `linux-arm64` binaries ship with every release too.

> **Back up `data/.env`** (or `/var/lib/mcpd/.env` on Debian). It holds the key
> that every stored credential is encrypted with — back up that directory and
> you've backed up the deployment.

## Get started

Once mcpd is running:

1. Add an integration.
2. Connect your ChatGPT account.
3. Create a secure tunnel.
4. Choose what that tunnel can reach.
5. Ask ChatGPT about your infrastructure.

> **Your infrastructure stays private. ChatGPT gets the access you choose.**

## Documentation

Detailed configuration, security, plugin development, approvals, and architecture documentation lives in [`docs/`](docs/).

---

**mcpd — private infrastructure, connected to AI.**
