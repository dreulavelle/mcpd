# Observium

What the API does that a reader would not expect. The plugin's design follows
from these; the host's own contract is in [architecture.md](architecture.md).

## A collection is an object, not an array

This is the one that breaks a client written from habit.

```json
{
  "status": "ok",
  "count": 2,
  "devices": {
    "277": { "device_id": "277", "hostname": "router-1.example.com" },
    "278": { "device_id": "278", "hostname": "router-b.example.com" }
  }
}
```

`devices` is a JSON **object keyed by the entity's database id**. Decoding it
into a slice fails outright. Decoding it into a map succeeds and then loses the
order, so the same call returns devices in a different sequence every time and
an assistant comparing two answers sees changes that never happened. Go
randomises map iteration deliberately, so this is not a bug that shows up
occasionally — it shows up differently on every call.

`decodeCollection` in `client.go` sorts by the key, numerically where the key
is a number. That restores the order Observium's own UI shows.

The key is named after the endpoint rather than uniformly: `/neighbours/`
answers under `neighbours`, `/alert_log/` under `alert_log`, `/address/` under
`addresses`. Every call site passes the key in, which is less clever than
deriving it and survives the API not being consistent.

**Empty is an array.** PHP encodes an empty associative array as `[]`, so an
endpoint with no results answers with `"sensors": []` where a populated one
answers with an object. Both shapes are handled; treating the array case as a
decode failure would turn "no sensors" into an error.

## A 200 can mean failed

Some errors arrive as HTTP 200 with `"status": "failed"` and a message in the
body. A client that checks only the status code hands the model an empty
collection, which reads as *there are none* rather than *you were refused* —
and those are opposite answers to a question like "are any sensors in alarm?".

`do` checks the envelope's own status on every response.

## 404 is not only absence

Observium reports an entity outside the account's permissions as **absent**
rather than forbidden. That is good security and a confusing thing to be told,
because it makes "no such device" and "not your device" indistinguishable from
the outside. Both error messages say so rather than picking one.

Several endpoints are gated on the account's *user level* instead: VLANs need
level 7, maintenance windows level 8. Those do answer 403, so a read-only token
on a low-privilege account is refused rather than shown less. The topology tool
treats a VLAN refusal as a partial answer — the neighbours it already fetched
are still the answer to most of the question.

## 429 means two different things

Observium throttles failed *authentication* with the same status it uses for
load: `api.auth_fail_limit` failures (default 10) inside
`api.auth_fail_window` seconds (default 300) returns 429 until the window
clears. Conflating the two would have somebody lowering their request rate to
fix a wrong password. The error names both possibilities and says which fix
belongs to which.

## Pagination is opt-in, and that is a performance decision

Without a `pagesize` parameter Observium returns the whole table in one
response. On a large estate that is a slow query building a large body, so
asking for pages is not only about bounding what a tool returns — it is about
not making the upstream construct the entire answer first.

`countpage` is set when Observium paginates, but not every endpoint sets it,
so the walk stops on the first short page and treats `countpage` as a second
signal rather than the only one. There is also a guard for an endpoint that
ignores `pageno` entirely: without it the walk would spin to `max_items`
making identical requests.

## There is no time-series API

Observium stores its metrics in RRD and does not serve them as data. There is
no JSON endpoint that returns a series. The only way the numbers come out is
`graph.php`, which renders **PNG images**.

This is why:

- The interface tool's traffic and error figures are **cumulative counters**,
  not rates. A rate needs two readings and this API does not serve the history,
  so the tool says so in its own description and in every result.
- `observium_graphs` returns links and states plainly that they are images the
  model cannot read. The failure mode otherwise is an assistant describing a
  trend it never saw, which is worse than having no trend tool at all.

**Graph links carry no credential.** `graph.php` authenticates with HTTP basic
auth or an existing browser session. The links are for a person to open, where
their own session answers for them. Embedding this plugin's credential in a URL
would put it in a chat transcript, a model's context window, and every log in
between.

**The graph type strings are mostly undocumented.** Observium publishes a
handful — `device_bits`, `port_bits`, `storage_usage` — and not the rest.
Guessing at the others produces links that render an error page, which is a
worse answer than a shorter list, so only the documented ones are offered by
name and anything else is passed through verbatim for a caller who has read one
off Observium's own UI. Devices are identified with `device=`; everything else
uses `id=`.

### If we ever want real trend data

It is reachable, but not through this API. `rrdtool fetch` or `rrdtool xport`
against the RRD files gives real series — which is how
[kdesch5000/observium-mcp](https://github.com/kdesch5000/observium-mcp) does
it, reading MySQL directly and shelling out to `rrdtool`.

That is a different deployment posture from the one mcpd has: it needs database
credentials and filesystem access to the Observium host, or an SSH tunnel to
it. Everything this plugin relies on — the read-only transport, the deny-list,
one revocable token — assumes an HTTP client talking to an HTTP API. Adopting
the other shape means giving up those guarantees, so it is a deliberate
decision rather than an extension, and it has not been made.

## Authentication

Two forms, and the API treats them as equivalent:

- **API token** (`Authorization: Bearer <token>`), from Profile → API tokens →
  Manage. Preferred: issuable read-only, scoped to the permissions of the
  account that made it, and revocable without changing anyone's login.
- **HTTP basic auth**, which is what an installation too old for tokens has.
  Supported because refusing it would make this plugin unusable on exactly the
  deployments most likely to be running Observium.

The token wins when both are set. Two credentials with one obviously preferred
is not a choice worth making per request.

## The write surface, and why it is refused

The plugin is read-only. `transport.go` refuses every method but GET, which is
the guarantee itself — a transport that will not write cannot be talked into it
by a tool that gets a path wrong.

`denylist.go` is separate and exists for the day that changes. Mutations are
intended; scheduled maintenance windows are the obvious first one, being
reversible and verifiable by reading them back, and they are deliberately *not*
on the list. What is on it is what must survive the read-only guard being
widened:

| | |
|---|---|
| `DELETE /devices/{id}` | Removes a device and, with `delete_rrd=1`, every metric ever recorded for it. Monitoring history is not reconstructible — there is no upstream to re-poll the past from. This is the entry the list exists for. |
| `PUT /devices/{id}` | Rewrites SNMP credentials and poller assignment. A device silently stops being polled and nothing looks broken until somebody needs the data. |
| `POST /devices/` | Starts SNMP traffic towards an address a model chose. |
| `PUT /alert_checks/{id}` | Disables an alert checker. The failure mode is an estate that reports healthy because nothing is checking it. |

Widen `transport.go` by naming what is newly permitted, not by inverting the
default.

## What is cached, and what is never

The split is between what an operator configures and what the poller writes. A
hostname changes when somebody renames the device; a sensor reading changes
every poll cycle.

| Class | Default | What |
|---|---|---|
| Inventory | 10 minutes | `/devices`, `/inventory`, `/vlans`, `/groups`, `/addresses`, `/neighbours` |
| State | 30 seconds | `/ports`, `/sensors`, `/status`, `/storage`, `/mempools`, `/processors`, `/counters`, `/probes`, `/printersupplies` |
| Never | — | `/alerts`, `/alert_log`, `/alert_checks` |

Alerts are never held. A model reads them to find out whether something is
wrong *now*, and a cached "no alerts" says the network is fine at the moment it
stopped being fine — the worst answer this integration can give. They are also
cheap next to a device walk, so there is nothing to buy.

The classifier is an allow-list: an endpoint it does not recognise is fetched
every time, so a tool added later cannot quietly start being served from
memory.

## Discovering the rest of the API

An authenticated client can fetch `/api/v0/openapi.json`, which is a
machine-readable specification of what that installation actually serves. It is
the right thing to check against when adding a tool, because it reflects the
version in front of you rather than the documentation's.
