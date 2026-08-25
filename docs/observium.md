# Observium

**This integration needs Observium's REST API, which is a subscription
feature.** Community Edition does not have one — `$config['api']['enable']` is
not a switch that turns it on there — so a CE installation cannot be read at
all. That is the first thing to check when nothing works.

mcpd read CE directly from its MySQL database for a while, and that support was
removed deliberately: it cost a second code path, a second set of filter
translations, and a permanent dependency on a schema Observium does not version
or promise anything about.

What follows is what the API does that a reader would not expect. The host's
own contract is in [architecture.md](architecture.md).

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

- The interface tool returns **both**: `ifInOctets` is a cumulative counter and
  `ifInOctets_rate` is the per-second figure Observium computed at the last
  poll. `poll_time` and `poll_period` say when that was, which is what lets a
  rate of zero be told apart from a rate nobody has recomputed lately.

  An earlier version of this document said the interface figures were
  cumulative counters and nothing else. That was wrong: Observium computes
  rates on every poll and stores them in the `ports` table, so current
  throughput never needed RRD. Only *history* does.
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

### If we ever want history

Current throughput does not need RRD — the `_rate` columns cover it on both
backends. What RRD holds that nothing else does is the *past*: what a port was
doing last Tuesday.

Getting it means `rrdtool fetch` or `rrdtool xport` against the files, which is
how [kdesch5000/observium-mcp](https://github.com/kdesch5000/observium-mcp)
does it. The design decision, if we take it, is that the RRD directory is a
**local filesystem path** — whether that path is a bind mount, an NFS mount or
sshfs is a deployment concern and not the plugin's, which keeps mcpd out of the
SSH-subsystem business. An empty path means no trends tool is registered, so
the capability declares itself rather than being assumed.

Not built. It needs `rrdtool` in the image or an RRD parser, and neither is
worth adding until somebody wants history badly enough to say so.

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

## Testing against a real installation

`integration_test.go` runs the whole plugin against a live Observium and skips
unless one is supplied:

```bash
OBSERVIUM_TEST_URL=https://observium.example.com \
OBSERVIUM_TEST_TOKEN=… \
go test ./internal/plugins/observium/ -run Integration -v
```

**It is worth more than the rest of the suite put together, and it has never
been run.** The database backend this replaced had every one of its filters
broken — `status=up` matched nothing, `state=up` was accepted and silently
dropped, an alert state was inverted — and all of it looked correct until these
tests ran against real data. Each failed the same way: a request that succeeds,
matches nothing, and returns an empty result which reads as an answer.

The API's documented vocabulary has not been checked against what a live
instance actually accepts. Run this first against any new deployment, and treat
a filter that returns zero as a bug until proven otherwise.

## Discovering the rest of the API

An authenticated client can fetch `/api/v0/openapi.json`, which is a
machine-readable specification of what that installation actually serves. It is
the right thing to check against when adding a tool, because it reflects the
version in front of you rather than the documentation's.
