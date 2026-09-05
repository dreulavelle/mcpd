# Features

[Back to the project overview](../README.md)

mcpd connects AI clients to the systems you allow, with one place to manage
integrations, access, and operation history. See the [quick start](../README.md#install)
to deploy it or the [integration list](../README.md#built-in-integrations) to
find a specific system.

## Private connectivity

- **Built-in ChatGPT tunnels.** Connect outbound without assigning mcpd a
  public IP, opening inbound firewall rules, or setting up public DNS.
- **Multiple ChatGPT accounts.** Each account has its own identity and
  access. A tunnel's grants are limited by its account's grants.
- **One gateway, or a connector per system.** Combine integrations behind
  one MCP address or connect to a single plugin instance.
- **Other MCP clients.** Use direct, API-key-authenticated connections from
  clients that can reach the MCP listener. The dashboard's Clients page
  prepares client configurations.
- **Your own certificate authorities.** Trust a company CA or a suitable
  self-signed appliance certificate without disabling TLS verification.
  Certificate-name checks still apply.

The outbound tunnel removes the need to expose services publicly; it does not
keep permitted tool results inside your network. Results returned to ChatGPT
leave the network through that connection.

See [Configuration](configuration.md) for client connections, credential
scoping, and certificate management.

## Changes with recorded authorization

- **Approval-gated mutations.** A mutation must have recorded approval
  before it executes.
- **Approval in the conversation.** Confirm a change where it was requested.
  The dashboard holds history, standing rules, and the audit trail.
- **Standing rules.** Pre-authorize eligible routine work by plugin, action,
  principal, and risk ceiling. Exclusions take precedence over grants.
- **Human review for irreversible changes.** Standing rules cannot authorize
  irreversible mutations, critical risk, or an unrecognized risk level.
- **Drift checks and outcome verification.** For mutations that supply the
  necessary evidence, compare live state before execution and re-read the
  system afterward. Missing evidence is not reported as a successful check.
- **Fine-grained access.** Scope users, groups, API keys, accounts, and tunnels
  to the plugin instances they need.

A **reviewed change** carries exact fields, detectable drift, and a confirmed
outcome. A **gated call** records authorization without claiming all three.
Whether a person or a standing rule authorized the operation is recorded
separately from what can be verified.

See [How a change gets made](approvals.md), [Approval policies](approval-policy.md),
and [Architecture](architecture.md) for the execution guarantees.

## Integrations and extensibility

- **Built-in integrations.** Connect monitoring, networking, telecom, and
  knowledge-base systems. The [integration table](../README.md#built-in-integrations)
  distinguishes read-only integrations from those with approval-gated changes.
- **Multiple instances.** Configure separate environments, customers, sites,
  or servers independently. Access is per instance, not per customer row
  inside an instance.
- **MCP marketplace.** Discover and import MCP servers from multiple catalogs.
- **Remote MCP servers.** Bring existing MCP servers under one managed,
  access-controlled gateway.
- **Tools, resources, and prompts.** MCP support extends beyond tool calls.
- **Go plugin SDK.** Build custom integrations in-process or as separate
  programs. Out-of-process plugins can be added without rebuilding mcpd.
- **Private catalogs.** Keep approved server definitions in a git repository
  and review changes before making them available.

See [Writing a plugin](plugins.md), the [SDK](../sdk), the
[Echo example](../examples/echo), and [Private catalogs](catalog.md).

## Day-to-day operations

- **Built-in dashboard.** Manage plugins, tunnels, users, permissions,
  approval history, logs, and system health from a browser.
- **Performance visibility.** Inspect per-tool latency, error rates, and cache
  behavior to identify slow integrations.
- **Tamper-evident audit history.** A hash-chained record tracks operation
  authorizations and transitions.
- **Tool-call history.** A separate ledger records who called what and how it
  ended; it does not store tool arguments or results.
- **Outbound notifications.** Send configured notifications about remote MCP
  tool-list changes and cancellations. See [Notifications](notifications.md).
- **SSO and local accounts.** Sign in with Google, Microsoft Entra, GitHub,
  or an OpenID Connect provider alongside local users.
- **API keys for automation.** Give scripts and agents their own identities
  and permissions.
- **Encrypted integration credentials.** Store secrets locally, encrypted
  under the instance's key. Protect that key and include it in backups.
- **Opt-in reporting.** Crash reporting and update checks are disabled until
  enabled by the operator.
- **Encrypted backup and restore.** Export an instance as one encrypted file
  for recovery or migration. See [Backup and restore](backup.md).

## Deployment

The dashboard, SQLite database, MCP host, and tunnel management run in one
binary. No separate database server or application runtime is required for
the published mcpd binary. External MCP servers and plugins may have their
own runtime requirements.

Deploy with Docker, a Debian package, or a standalone Linux binary. See
[Install](../README.md#install) and [Upgrading](upgrading.md).
