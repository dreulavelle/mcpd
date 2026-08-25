# Observium

The plugin reads one Observium estate two ways, and which one you get is
decided by the licence rather than by preference.

**The REST API is a subscription feature.** Community Edition does not have
one — `$config['api']['enable']` is not a switch that turns it on there — so
on CE the only way in is the database Observium writes to. That is what the
`backend` setting selects, and it is why there are two of everything below.

The dashboard offers that choice as **Community Edition** or **Subscription**,
because that is the question an operator can answer — nobody knows offhand
whether they want the API or the database, and everybody knows which licence
they bought. The stored values stay `database` and `api`: configuration should
record what actually changes, and the two audiences are different enough that
one string cannot serve both.

| | API backend | Database backend |
|---|---|---|
| Needs | Subscription Edition | Any edition |
| Reaches | `https://…/api/v0` | MySQL, port 3306 |
| Credential | API token, or basic auth | A MySQL account |
| Read-only proved by | A transport refusing every method but GET | The account's own grants, read back at startup |
| Sees | What one Observium account may see | Everything in the schema |
| Graph links | Yes | No — there is no web address to build them from |
| Per-second rates | Yes | Yes |

Neither can write. What differs is what each can *prove* about that, which is
why `Describe` on both says which guarantee is theirs rather than both
claiming "read-only" and leaving the difference implicit.

What follows is what each does that a reader would not expect. The host's own
contract is in [architecture.md](architecture.md).

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

## The database backend

### Read-only is proved by MySQL, not by us

The API backend's guarantee is `transport.go` refusing every method but GET —
structural, and impossible to talk past. There is no HTTP here, so that
guarantee does not carry over. What replaces it is `checkGrants`, run at
startup: the account's own grants come back from `SHOW GRANTS FOR
CURRENT_USER()` and anything beyond `SELECT` refuses the connection.

That is *stronger* in one way — the guarantee rests on the database server, so
a bug in this package cannot widen it — and weaker in another, which is worth
being plain about. A read-only API token is scoped to Observium's permission
model and sees what one Observium account may see. A MySQL account with
`SELECT` on the schema sees everything, including the SNMP community strings in
`devices` and the password hashes in `users`, neither of which this reads.
Least privilege is the operator's job here in a way it is not with a token.

Two details in the check that exist because of specific failures:

- The privilege list ends at `ON`. Everything after is a database name and a
  host, so a schema called `create_backup` or a host called
  `insert.example.com` would otherwise refuse a perfectly restricted account —
  and a false refusal is one nobody can debug from the message.
- `WITH GRANT OPTION` is checked against the whole line, because it is written
  *after* the `ON` clause. Truncating first would miss the one privilege that
  lets an account give itself the others.

If `SHOW GRANTS` is itself refused — some managed MySQL services do this — the
plugin logs a warning and continues. Refusing to start would be wrong, since
the account may well be correctly restricted; saying nothing would be worse,
because the guarantee is then simply absent.

### The schema is not a contract

Observium versions its API. It does not version its schema, and columns move
between releases with no compatibility promise. So every column these queries
name is checked against `information_schema` at startup, and a mismatch names
the column. The alternative is a MySQL error inside the first tool call an
assistant makes, about a table nobody reading it has heard of.

Columns are named explicitly rather than `SELECT *`, for three reasons in this
order: `devices` holds SNMP community strings and auth passwords, and a
wildcard would hand them to a model; a wildcard makes a schema change silently
alter what a tool returns; and naming them is what makes the startup check
possible at all.

### The API's words are not the schema's values

This is the part that cannot be got right by reading documentation, and every
line of it was wrong until the queries were run against a real installation.
The failure mode is identical in each case: a query that runs, matches nothing,
and returns an empty result which reads as an answer.

| The API says | The schema holds | |
|---|---|---|
| `status=up` on a device | `status` is a `tinyint`, `1` | plus `disabled` as a separate column, so one filter reaches two |
| `status=down` | `status = 0 AND disabled = 0` | "down" and "disabled" are different states |
| `event=warn` on a sensor | the enum's value is `warning` | `warn` matches nothing |
| `status=failed` on an alert | `alert_status = 0` | **zero is the failing state**, confirmed from Observium's own `alerts.inc.php`, which writes `'0'` beside `last_message = 'Checks failed'`. Inverting this reports a healthy estate as broken |
| `state=up` on a port | `ifOperStatus`, an enum whose values *are* the API's words | the one that needs no translation |
| `errors=1` on a port | no such column | the rate being non-zero — the cumulative counter is non-zero for any interface that has ever had one |
| `alerted=1` on a port | no such column | a subquery against `alert_table` |
| `timestamp_from` | `eventlog.timestamp` is a `timestamp` | the argument is a unix epoch, so `FROM_UNIXTIME` |

Note what the tools return, which is a separate question: `status` comes back as
`1`, not `"up"`, on **both** backends — the API serves the same row. The filter
vocabulary is translated; the values are not rewritten, because doing so on one
backend and not the other would make the same estate look different depending
on the licence.

### A filter that cannot be applied is refused

Not dropped. The two are opposite mistakes, and the dangerous one looks
harmless: dropping `status = down` does not narrow the result, it returns every
device presented as though it had been filtered, and an assistant asked which
devices are down then names all of them.

**Output options are different and are ignored.** `expand_entities`,
`humanize`, `fields` and their like shape what comes back rather than narrowing
what matches, so a backend that cannot honour one has still answered the right
question. `outputOptions` in `reader.go` is that list, and the distinction is
the reason it exists.

### Soft deletes

Observium does not delete rows. A port pulled out of a switch stays in `ports`
with `deleted = 1`, and the same is true of sensors, storage, memory pools and
inventory. Every query filters on it — without that, the tools report hardware
that no longer exists as though it were live.

### Two tables, one entity

The API serves IPv4 and IPv6 under one `addresses` key. The database keeps them
in `ipv4_addresses` and `ipv6_addresses` with column names that differ for a
reason, so `Read` queries both and concatenates, with the second half bounded
by whatever headroom the first left. One ceiling, not two.

### What comes back

MySQL hands back `[]byte` for text and for anything it is unsure of, and a
`[]byte` marshals to base64 — so a hostname would reach the model as
`cm91dGVyLTEubG9jYWw=` without conversion. Numbers stay numbers, because a
model asked whether a disk is over 90 per cent should be comparing against a
number rather than against text. Timestamps become RFC 3339 in UTC.

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

`integration_test.go` runs the whole backend against a live Observium and
skips unless one is supplied:

```bash
OBSERVIUM_TEST_DB_HOST=… OBSERVIUM_TEST_DB_NAME=observium OBSERVIUM_TEST_DB_USER=mcpd_ro OBSERVIUM_TEST_DB_PASSWORD=… go test ./internal/plugins/observium/ -run Integration -v
```

It is worth keeping because the half of this package a fake cannot reach is the
half that was wrong. The grant check, the schema check and every filter above
are claims about somebody else's database; a fake agrees with whatever the code
believes.

Setting up the account it needs:

```sql
CREATE USER 'mcpd_ro'@'<mcpd host>' IDENTIFIED BY '…';
GRANT SELECT ON observium.* TO 'mcpd_ro'@'<mcpd host>';
```

If MariaDB is bound to `127.0.0.1` — the Debian default — and mcpd is on
another machine, it cannot reach it. Either bind to the LAN address and grant
from the one host, or tunnel; the first is simpler and puts a database on the
network, so it is a decision rather than a step.

## Discovering the rest of the API

An authenticated client can fetch `/api/v0/openapi.json`, which is a
machine-readable specification of what that installation actually serves. It is
the right thing to check against when adding a tool, because it reflects the
version in front of you rather than the documentation's.
