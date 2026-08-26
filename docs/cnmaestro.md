# cnMaestro

Notes on Cambium's cnMaestro API, kept because they cost real effort to
establish and none of them is obvious from the reference. This is a plugin
document, not architecture — how mcpd is built is in
[architecture.md](architecture.md).

Reference: <https://docs.cloud.cambiumnetworks.com/api/latest/index.html>

The page is a Swagger UI and renders nothing to a fetch. The spec behind it is
`yaml/schemas/v2/base.yaml` under that path, with `yaml/responses/v2/…` for
response shapes and `announcements.json` for what changed per release. Those
are worth reading directly; the announcements in particular carry deprecations
the reference itself does not mention.

Base path is `/api/v2`. Current version is 6.3.0.

## Authentication

OAuth 2.0 client credentials. Credentials come from the cnMaestro UI, under
API Clients, via **Download Credentials** — a client id and a client secret.

```
POST /api/v2/access/token
Authorization: Basic base64(client_id:client_secret)
Content-Type: application/x-www-form-urlencoded

grant_type=client_credentials
```

The credentials may instead be sent in the body as
`grant_type=client_credentials&client_id=…&client_secret=…`. The Basic header
is preferable: it keeps the secret out of anything that logs a request body.

The response is an ordinary token response with one addition:

```json
{
  "access_token": "…",
  "token_type": "bearer",
  "expires_in": 3600,
  "redirect_uri": "https://<region>.cloud.cambiumnetworks.com"
}
```

**`redirect_uri` is load-bearing.** Cloud accounts are regionally sharded, and
the token response names the host that subsequent calls must actually target.
Authenticating against `cloud.cambiumnetworks.com` and then calling it for data
is the classic first-integration failure: the token is valid and the calls go
to the wrong shard.

Tokens last an hour. Refresh ahead of expiry rather than on a 401, so
credential handling stays out of the error path where it would be tangled with
retries and rate limiting.

## Endpoints that execute code on a device

These are reachable with the same account-wide token as every read, and none of
them is needed to manage a network:

```
POST /devices/{mac}/cli                      arbitrary CLI execution
POST /cnwave60/devices/{mac}/remote_command  the 60 GHz equivalent
```

The second did not exist in API 5.0.1 and appeared in 6.3.0. That is the
argument for enforcing a deny-list in code rather than in a design document: a
list that lives in prose does not survive the API growing a new way to run
commands.

Lower individually but unbounded together, and equally unnecessary:

```
POST /devices/{mac}/ping
POST /devices/{mac}/traceroute
POST /devices/{mac}/pull_config
POST /devices/{mac}/wifi_perf
POST /cnwave60/devices/{mac}/ping
POST /cnwave60/devices/{mac}/iperf
POST /cnwave60/devices/{mac}/links/{id}/iperf
POST /cnwave60/devices/{mac}/topology_scan
```

## What mcpd exposes

Seventeen read tools, no writes. Grouped by the question they answer rather
than by the endpoint they call, because a model picks a tool by what it wants
to know:

| tool | endpoint |
|---|---|
| `networks`, `devices`, `device` | `/networks`, `/devices`, `/devices/{mac}` |
| `managed_accounts` | `/msp/managed_accounts` |
| `sites`, `towers` | `/networks/{network}/sites`, `/networks/{network}/towers` |
| `alarms`, `alarm_history`, `events` | `/alarms`, `/alarms/history`, `/events` |
| `clients`, `wired_clients`, `mesh_peers` | `/devices/clients` or `/devices/{mac}/clients`, `/devices/wired_clients`, `/devices/mesh/peers` |
| `statistics`, `device_statistics`, `device_performance` | `/devices/statistics`, `/devices/{mac}/statistics`, `/devices/{mac}/performance` |
| `wlans`, `ap_groups` | `/wifi_enterprise/wlans`, `/wifi_enterprise/ap_groups` |

Four parameter facts that are not guessable from the reference, each of which
costs a wrong answer rather than an error:

- **The spec's parameter names are not always the wire names.** `event_severity`
  is sent as `severity`, and `deviceType` is sent as `type`. A client that sends
  the reference's name filters nothing and returns everything.
- **`/devices/{mac}/performance` requires `start_time` and `stop_time`.** They
  are not defaulted.
- **`/devices/clients` filters by `client_type` and nothing else** — no network,
  no site. Narrowing means asking a single access point instead.
- **A malformed timestamp returns an empty result rather than an error**, which
  reads as "nothing happened then". Timestamps are parsed before the call.

## Pagination

Two schemes, and which one applies is per endpoint rather than global. An
earlier note here said continuation tokens were the rule; the announcements
say otherwise, and building on that would have paginated most collections with
a parameter they do not accept.

**`limit` and `offset`** for most collections — `/devices`, `/networks`,
`/sites`, `/towers`, `/alarms`.

**`continuation_token`** for four, where `offset` is deprecated in 6.3.0 and
removed in 6.4.0:

```
GET /events
GET /devices/{mac}/performance
GET /devices/nse/{mac}/threats
GET /cnwave60/devices/{mac}/links/{link_name}/performance
```

Send the first request without `continuation_token`, then repeat with it set to
the previous response's `next_continuation_token` until a response carries
none. `offset` and `total` are still returned on the first response for
backward compatibility, so a client that reads them will appear to work right
up until 6.4.0.

Prefer the continuation token wherever it is offered regardless: offset paging
is unsound while an estate is changing, because rows shift between pages as
devices come and go.

## Reusing an answer

A listing walks every page, so asking twice in one conversation walks the whole
estate twice. Some of those answers may be held for a moment and some may not,
and the line between them is not how expensive they are.

**Held.** How the estate is *arranged*: `/networks`, `/msp/managed_accounts`,
`/networks/{n}/sites`, `/networks/{n}/towers`, `/wifi_enterprise/wlans`,
`/wifi_enterprise/ap_groups`. These change when a person changes them, which is
not something that happens between two tool calls. Five minutes by default
(`inventory_cache_seconds`).

**Held briefly.** `/devices` and one device by address. Fifteen seconds by
default (`device_cache_seconds`), and short for a reason worth stating rather
than out of caution: a device's state is a premise a model reasons from, so a
stale one is a correctness problem and not a freshness one. What makes any
window defensible is that cnMaestro's own view is already behind — the
controller learns a device has gone offline on its own polling interval,
measured in minutes — so fifteen seconds adds nothing measurable to an error
that is already there, while removing the second and third full walk of one
estate inside a single conversation. Set it to zero if that trade is not one
your deployment wants.

**Never held**, whatever either setting says:

| | why |
|---|---|
| `/alarms`, `/alarms/history`, `/events` | Asked in order to find out whether something is wrong *now*. A cached "no alarms" says the network is fine at the moment it stopped being fine. They are also cheap next to a device walk, so there is nothing to buy. |
| `/devices/clients`, `/devices/{mac}/clients`, `/devices/wired_clients`, `/devices/mesh/peers` | Who is connected changes second to second, and is asked in order to act on it. |
| `/devices/statistics`, `/devices/{mac}/statistics`, `/devices/{mac}/performance` | Readings rather than records, usually over a window ending at "now" — so the key would differ on every call, and a cache would hold memory it never answered from. |
| anything else | Deny by default. An endpoint added to the client later is fetched every time until somebody decides otherwise. |

The key is the endpoint plus the fully resolved query, including the account
this request will actually read from. Two callers who name the same account —
one explicitly, one by leaving it to the configured default — ask one question
and share one answer; two who name different accounts never do. Nothing about
the caller is in the key, and nothing needs to be: every caller of one
configured instance reaches cnMaestro with the same credential, so identical
keys mean identical upstream requests. Two configured instances hold two
separate caches.

A miss is shared rather than duplicated: six tool calls that all need the
device list arrive together and cost one walk. A failure is never held — an
upstream that is down is reported as down on every call, rather than remembered
as an empty estate.

## Response envelope

```json
{
  "paging":   { "offset": 0, "limit": 100, "total": 42,
                "next_continuation_token": "…" },
  "warnings": ["…"],
  "data":     [ … ]
}
```

`warnings` is easy to miss and worth surfacing: the API answers 200 with a
partial result rather than failing when part of an estate is unreachable.

`data` for `/devices` is a `oneOf` across device types — cnmatrix, cnwave60,
enterprise Wi-Fi, NSE and others — each with its own fields and a `type`
discriminator. There is no common device shape to decode into.

## Getting API credentials

**Services → API Clients → Add API Client**, then *Download Credentials* for
the client id and secret. Two things gate the page:

- **A Super Admin account.** Lesser roles do not see it.
- **cnMaestro X.** The RESTful API is an X capability. On Essentials the page
  is not hidden, it is not there, so "I cannot find it" and "I am on the free
  tier" look identical from inside the UI.

If the menu is missing, those are the two things to check, in that order.

**MSP multi-tenancy is also an X feature**, which makes the two facts useful
against each other. An installation genuinely running MSP tenants already has
X, and therefore already has the API — so a missing API Clients page on an MSP
installation points at the role, or at being signed in to a managed account
rather than the parent, rather than at the subscription. Conversely, if the
page really is absent for a Super Admin on the parent account, what is in use
is probably several separate accounts or one account with several networks,
rather than MSP tenancy.

On an MSP installation the API client belongs to the parent account. One client
reaches every tenant, and which tenant a request reads from is decided by
`managed_account` rather than by having separate credentials per tenant.

## managed_account

Every request either names an account or does not, and the difference is the
parameter most likely to produce a plausible wrong answer rather than an error.

mcpd sends it when a tool call names an account, and otherwise when the
instance has one configured as its default. The configured value is a default
rather than a confinement: `cnmaestro_list_devices`, `cnmaestro_list_networks` and
`cnmaestro_get_device` each take an `account` argument, so one instance answers
questions about any tenant its credential can see. Leaving the setting empty is
the arrangement for an MSP installation -- reads then span every account, and
the assistant narrows by naming one. Each result reports which account answered.

It takes an MSP tenant name, or the reserved value `Base Infrastructure`
meaning the Main Account. Matching is exact and case-sensitive;
`base infrastructure` is rejected.

**Omitting it is not the same as naming the Main Account.** The default depends
on whether the request names a network, not on which endpoint is called:

| request | reads from |
|---|---|
| `GET /devices` | every account |
| `GET /devices?network=Campus-1` | Main Account only |

`site` and `tower` are rejected unless `network` is supplied, so every
hierarchy-filtered request takes the Main Account default. Two tool calls
differing only by a filter would otherwise read from different accounts, which
is not a failure anyone notices -- so when no account was named, the device
listing says in its `note` which of the two happened.

**Reading differs from writing.** Objects in the Main Account report
`"managed_account": ""`, and that empty string is never valid to send — sending
it is treated as omitting the parameter. So the value that selects the Main
Account is not the value read back from objects in it.

Failures, all against ordinary reads:

| status | means |
|---|---|
| `404 managed_account not found` | no tenant by that name, or wrong case |
| `403 managed_account is disabled` | the tenant exists and rejects every call |
| `400 MSP feature is disabled` | not an MSP account; only `Base Infrastructure` is accepted |

`GET /msp/managed_accounts` is the authoritative tenant list and the only place
each tenant's `status` is exposed. A tenant can own visible data and still
reject every call that names it, so a listing that looks right is not evidence
the tenant is usable.

Path-style MSP filtering — `/msp/managed_accounts/{name}/…` — is deprecated for
GET, POST and PUT, and will be removed. The query parameter is the form with a
future.

## Unverified

**Whether `PUT /devices/{mac}` merges or replaces the `overrides` object is
undocumented.** Resending every override that was read is correct under either
behaviour, and is what the previous implementation did. Confirm against real
hardware before the first production write; a replace-semantics API silently
discards settings a merge-semantics client assumed it was leaving alone.

Everything here was established against the published 6.3.0 specification and
a fake controller. None of it has been exercised against a live controller.
