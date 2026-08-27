# ExtremeCloud IQ

What this integration talks to, and the things about ExtremeCloud IQ's API that
a reader would otherwise have to find out by being surprised.

Written against the API as it stands in the 25.12 release. Where a version
matters it is named.

Read-only. The guarantee is enforced in `transport.go`, and it is an
allow-list rather than a method check, and this API makes the reason
unusually sharp in both directions — see [A method check would be wrong in
both directions](#a-method-check-would-be-wrong-in-both-directions).

## The tools

Fourteen, grouped by the question somebody asks rather than by the endpoint
that answers it. This API has 293 GET reads and about sixty more reached by
POST; a model asked "is the Springfield wireless working" should not be
choosing between forty of them.

They divide in two, and the division is the useful part. Nine say what exists
and what state it is in. Five say what is *wrong* — and "is this access point
up" and "is this access point coping" are different questions that a single
tool would blur.

**What exists**

| Tool | Answers | Endpoint |
|---|---|---|
| `get_estate_summary` | "how is the network" | `GET /devices/stats`, `/clients/summary`, `/alerts/count-by-SEVERITY` |
| `list_devices` | "what have we got, and is it up" | `GET /devices` |
| `get_device` | "tell me about this access point" | `GET /devices/{id}`, `…/location`, `…/network-policy` |
| `get_device_health` | "that AP is slow, and nothing says why" | `GET /devices/{id}/interfaces/wifi`, `…/history/cpu-mem`, `…/alarms`, `/d360/device/issues` |
| `list_clients` | "who is connected" | `GET /clients/active` |
| `list_alerts` | "what has gone wrong, and how bad is it" | `GET /alerts`, `/alerts/count-by-SEVERITY` |
| `list_audit_logs` | "it worked yesterday — what changed" | `GET /logs/audit` |
| `list_locations` | "what are the sites called" | `GET /locations/tree` |
| `list_network_policies` | "what is this network meant to be doing" | `GET /network-policies`, `/ssids` |

**What is wrong**

| Tool | Answers | Endpoint |
|---|---|---|
| `list_device_issues` | "which APs or switches are not coping" | `POST /dashboard/{wireless,wired}/{device-health,usage-capacity}/grid` |
| `list_client_issues` | "which clients are failing, and at which step" | `POST /dashboard/{wireless,wired}/client-health/grid` |
| `get_client_history` | "why does this laptop keep dropping" | `GET /client-details/overview/info/{id}`, `…/connectivity-experience/{id}`, `…/roaming-trail/grid/{id}` |
| `list_sites_with_issues` | "where should I be looking" | `POST /dashboard/sites-with-issues`, `GET /network-scorecard/*/{id}` |
| `list_anomalies` | "what has the platform noticed by itself" | `GET /copilot/anomalies/anomalies-by-category` |

The grouping is for clarity, not for context. These fourteen cost about
twenty-two and a half kilobytes on every conversation whether they are called or not,
and grouping does not reduce that — the composite results carry output schemas
larger by roughly what the extra tool entries would have cost. See
[Tool list budget](plugins.md#the-tool-list-is-a-budget).

## One address, whichever region the account is in

`https://api.extremecloudiq.com` is regionless. ExtremeCloud IQ shards accounts
across regional data centres, and the endpoint routes to wherever the account
actually lives — so there is one address, it is the default, and an operator
should not normally change it.

This is worth stating because the neighbouring integration in this repository
works the other way round. cnMaestro issues a token at the front door and names
the shard to use in the token response, and a client that authenticates at the
front door and then reads from it holds a valid token pointed at the wrong
region: nothing looks wrong, the reads just return nothing useful. Nothing
equivalent can happen here.

What *can* happen is two tokens on the same address reading two entirely
different estates, which is why the data centre is named in the startup log.
When somebody reports an answer that does not match what they see in the web
interface, that line is the first thing to check.

## A method check would be wrong in both directions

**It would refuse reads this integration needs.** The whole diagnostics half of
this API — which clients are failing authentication, which switches have a PoE
fault, which sites are unwell — is reached by POST, because the filter is a
list of site and device ids that does not fit in a query string. That is the
same shape Graylog's searches have, and a "GET only" transport would refuse
every one of them.

This was very nearly missed. The first version of this plugin enumerated the
API's GET endpoints and stopped there, shipping nine tools that could say what
existed and nothing about why any of it was broken. About sixty reads live
behind POST and none of them appear in a listing of GETs.

**It would also permit reads that are worse than writes.** The API has 520
paths, 293 of them GETs, and among those are:

```
GET /account/viq/default-device-password
GET /acct-api-token/export
GET /packetcaptures/files
GET /endusers
```

The default password every device on the estate is onboarded with; every API
token in the account, exported to CSV; captured packets; the end-user
directory. Each is a read in the HTTP sense and a credential dump in every
other, and a method check permits all four — and would go on permitting
whatever the next release adds beside them.

So `transport.go` holds a named list, and a request is refused unless its
method *and* its path are both on it. It names what is reached and nothing
else: `/locations/site` and `/account/home` were on it briefly and both went,
because the location tree and the token probe already answered what they were
for. A permission granted in advance for a read nobody has argued for is the
habit the list exists to prevent.

The patterns are anchored and device ids are matched as digits, not as
`[^/]+` — that is what keeps `^/devices/[0-9]+$` from also permitting
`/devices/{id}/gallery-image`, and leaves `/devices/stats` to be decided by the
rule that names it rather than by a pattern aiming at something else.
`TestGuard_AdmitsTheNamedGridsAndNoOtherPost` pins the POST half: the seven
grids pass, and `POST /devices/:reboot`, `/auth/apitoken`, `/dashboard/export`
and the rest do not.

## Time is epoch milliseconds, and it is mandatory

Alerts, audit logs, device alarms and every history series take `startTime` and
`endTime` as **required** query parameters, in **milliseconds** since the
epoch. There is no "recent", no default, and no phrase parser: a call that
omits them is a 400.

Two things follow.

**Every tool sends a window whether the caller named one or not.** The default
is a day, which is the answer to "what has gone wrong today" — the question
somebody is asking when they do not say.

**Every result says which window it covered.** A count without the window it
covers is a number with no unit, and a model that has to infer the window will
infer the one in the question rather than the one in the answer.

The unit is the trap. Seconds is what everybody reaches for, and a window a
thousand times too narrow does not fail — it returns nothing, from a moment in
January 1970, which reads exactly like an estate with no alerts.
`window.apply` is the one place the conversion happens and
`TestWindow_IsSentInMilliseconds` pins it.

The audit log additionally refuses a window wider than 30 days. That limit is
enforced here rather than left to the API, because the API's refusal is a 400
naming a parameter and a number of milliseconds, which reads as a malformed
request rather than as a window to narrow.

## Fields are chosen, not returned

A device carries forty fields and a client fifty-six. `views=FULL` returns all
of them, and a hundred devices in FULL is most of a conversation.

The API's own answer is named subsets — `BASIC`, `STATUS`, `LOCATION`,
`DETAIL`, `FULL`, and for clients also `METRICS` and `IOT`. This integration
exposes that choice as a `view` argument rather than hiding it, and defaults to
the narrow end. A word outside the vocabulary is refused *with* the vocabulary:
a caller who asked for metrics and silently got identity fields would report
the absence of a health score as a fact.

It is also why a row is a `map[string]any` rather than a struct per collection.
The fields depend on the view, the set changes with the release, and a Go
struct would be a fourth description of the same thing — out of date the moment
Extreme adds a field, and four hundred lines of output schema a model pays for
on every conversation.

## A page is a hundred, and an estate is not

Every collection is paginated, at a hundred rows on most endpoints, five
hundred on audit logs and two thousand on network policies. A large estate is
tens of pages, and a walk is the shape most likely to trip a rate limit —
ExtremeCloud IQ meters API calls per account per hour, so that budget is shared
with everything else the account is running.

`Client.Collect` walks pages until one of three ceilings stops it, and **the
result says which one**:

- the caller's `limit`,
- the operator's `max_items`,
- the size of one answer, which is `plugins.ResultBudget(n)` divided by however
  many collections the result carries.

"Here are 200 of 4,317 devices" is an answer somebody can narrow. Two hundred
devices with nothing said about the rest is a wrong answer to "how many access
points do we have", and a model has no way to tell the two apart.

The walk ends on a short page as well as on `total_pages`, because several of
these collections leave `total_pages` out entirely and a walk that only trusted
it would loop to the row limit on every one of them.

## Names in, ids out

Every filter in this API is a numeric id. A model has a name — a site from a
ticket, a serial from an asset list, a MAC from a switch table — and the ids
appear nowhere else. So the tools take names and resolve them.

That resolution has a failure mode worth being careful about: **an id guessed
wrong is a filter that silently matches nothing**, not an error. An assistant
that guessed would report an empty site rather than a mistake.

So an ambiguous name is refused with the candidates named, and the two
resolvers differ on one point:

- `deviceID` reads an all-digit value as an id. Nothing else here is purely
  numeric in practice — an Extreme serial carries letters, a MAC is written
  with colons — and the tool descriptions say so, because the one shape that
  would collide is a MAC typed without separators.
- `locationID` does **not**. Floors are called "1" and "2" in every building
  anybody has ever named, so a number there is at least as likely to be a name
  as an id. Both readings are gathered, and `"1"` matching two floors is
  refused with both paths quoted rather than resolved to the site whose id is 1.

The full path — `Springfield/Main/1` — is what disambiguates, which is what
makes that refusal actionable rather than a dead end.

## What is cached, and what is never

The rule is an allow-list in `Config.cacheTTL`: an endpoint it does not
recognise is fetched every time, so a tool added later cannot quietly start
being served from memory.

Held, for `cache_seconds` (ten minutes by default):

```
/locations/tree   /network-policies
/network-policies/{id}/ssids        /ssids
```

These change when a person changes them.

Never held, whatever the setting says:

| | Why |
|---|---|
| `/devices`, `/devices/stats` | Whether an access point is connected is the most common thing anybody asks this integration, and a held answer to it is indistinguishable from a true one |
| `/clients/active` and the counts | Who is connected changes by the second |
| `/alerts`, `/logs/audit` | Read precisely when somebody suspects something is wrong, which is the moment a held answer is wrong |
| every per-device history series | A window ending *now*; a cached one is a window ending whenever it was fetched |
| `/auth/apitoken/info` | It is the startup probe, and a liveness check answered from memory is not one |

`TestReadCache_HoldsArrangementAndNothingElse` pins both halves.

The cache key is built from the request that will actually be made, with no
principal in it. That is safe **because** every caller of one instance reaches
the API with the same token, so two callers producing the same key produce
byte-identical upstream requests. If the credential ever became per-caller —
so that ExtremeCloud IQ's own scopes did the filtering — this shape would
become an access-control hole and the principal would have to enter the key.

## The credential, and the page that does not work

**Make the token in Extreme Platform ONE**, at
<https://extremeplatformone.com>, under your profile's API keys. Give it
read-only scopes. It is sent as `Authorization: Bearer …` — unlike Graylog's,
where an access token goes in the *username* field of a basic-auth pair, this
one is exactly what it looks like.

**Not** Global Settings → API Token Management inside ExtremeCloud IQ. That
page issues tokens for the **v1** API, retired for most tenants in January
2024, which is why it asks for a Client ID and rejects any name you type: it
wants an identifier Extreme issued for a registered v1 application. Following
it is a dead end that looks like a permissions problem, and it is the first
thing anybody setting this up will find, because it is the only page in the
product with "API Token" in its name.

There are three surfaces with confusingly similar names, and only the first is
relevant:

| | |
|---|---|
| `api.extremecloudiq.com` | The ExtremeCloud IQ v2 API. **This is what the plugin speaks.** |
| `cloudapi.extremecloudiq.com` | Extreme Platform ONE's gateway — a newer, separate surface (IAM, Site, Asset, Device Lifecycle, Client, Alert, Audit Log, Performance Monitoring, MetaStore). Not used here. |
| the v1 API | Retired. What API Token Management issues for. |

Platform ONE's developer portal documents both surfaces — ExtremeCloud IQ
appears there under *Applications* rather than *Services* — and a key made in
Platform ONE authenticates against `api.extremecloudiq.com`. That is verified
behaviour rather than an inference from the documentation, which says nothing
about it either way.

Two other ways to get a credential exist and are not offered:

`POST /login` with a username and password returns a JWT valid for 24 hours,
and `POST /auth/apitoken` mints a long-lived one while holding it. That is the
only path if a tenant has no Platform ONE access. It is not a settings field
here for three reasons, and the third decides it: a password carries everything
that account can do rather than what a token was scoped to; revoking it changes
somebody's login; and a tenant that signs in through an identity provider has
no password the API would accept, so the field would be offering a credential
that cannot work.

**The startup probe reads the token, not the estate.** `GET /auth/apitoken/info`
settles the four things a wrong configuration could be — the address does not
resolve, TLS fails, the token is refused, or something that is not the API
answered — and needs no permission to see a single device. It also returns the
expiry, which is the one thing worth saying *before* it happens: afterwards the
API answers 401, indistinguishable from a revoked token, and somebody spends an
afternoon on it. Both the startup log and the health report say so from two
weeks out.

## An empty result is not a failure, and says so

Three empty collections read like a call that went wrong. So
`get_device_health` on a device with nothing to report says in words that
ExtremeCloud IQ holds no samples or alarms for it in that window, and names the
two ordinary reasons — a device just onboarded, or one offline for the whole
window.

The same principle drives the partial answers. `get_device` composes three
reads and `get_estate_summary` composes three more; each part is best-effort
and a failure becomes a **named warning** rather than a failed call. A token
without the scope to read network policies should still be told about the
access point somebody asked about. The exception is a summary where *every*
part failed, which is not a partial answer — it is a broken connection wearing
the shape of one, and it is returned as an error.

## Alert counts survive truncation

`list_alerts` fetches `/alerts/count-by-SEVERITY` over the same window as the
listing, and returns both. The counts cover the whole window whatever the
listing was cut to, so "how bad is it" is answered even when "what exactly
happened" is not.

It also sends `sortField=TIMESTAMP&order=DESC` explicitly. The API defaults to
that and documents it, but a default is a thing that changes — and an assistant
reading the first ten of a thousand alerts must be reading the ten that just
happened.

## Logging

Debug is what a support call turns on. It carries the path, the status, the
byte count and the elapsed time, and **never the body and never the query**: a
successful body here is somebody's estate — hostnames, MAC addresses, the names
of the people connected to it — and the query names their sites.
`TestClient_DebugLogsCarryNoEstateData` pins it, including that the token never
appears.

## Testing against a real installation

```bash
TOKEN=…   # Global Settings > API Token Management

# The probe this plugin makes at startup, by hand. Reads no estate data.
curl -s -H "Authorization: Bearer $TOKEN" \
  https://api.extremecloudiq.com/auth/apitoken/info | jq

# A device listing, as the plugin sends it.
curl -s -H "Authorization: Bearer $TOKEN" \
  'https://api.extremecloudiq.com/devices?views=BASIC&page=1&limit=100' | jq '.total_count, .data[0]'

# Alerts over the last day. Note the milliseconds.
NOW=$(( $(date +%s) * 1000 )); DAY=$(( NOW - 86400000 ))
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://api.extremecloudiq.com/alerts/count-by-SEVERITY?startTime=$DAY&endTime=$NOW" | jq

# A diagnostics grid. The filter is a body and the paging is a query string,
# which is the API's own arrangement rather than a choice made here.
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  'https://api.extremecloudiq.com/dashboard/wired/client-health/grid?page=1&limit=10' \
  -d '{}' | jq '.total_count, .data[0]'
```

The whole OpenAPI document is at `https://api.extremecloudiq.com/openapi`, and
the rendered reference at
<https://extremecloudiq.com/api-docs/api-reference.html>.

## What is not implemented, and why

**Every write.** Onboarding, rebooting, CLI push, configuration deployment,
policy edits, packet capture. This integration is read-only; a write would be a
`MutationSpec` with a risk level, a drift check and an approval path, and none
of that exists here yet.

**Copilot's individual anomalies.** `list_anomalies` returns the counts, by
site, severity and kind, because that read needs only a window. The
drill-downs — PoE flapping trends, port-efficiency statistics, DFS recurrence —
each require an `anomalyId` that only appears in a listing this API does not
expose, so a tool for them would be a tool that cannot be called. They are also
almost entirely undocumented: no summary, no described response.

**ExtremeLocation** (`/essentials/eloc/…`). Client position on a floor plan, to
the metre. It is a genuinely different privilege from "who is connected", and a
tool that returned where a named person is standing should be a deliberate
decision with its own capability rather than the fifteenth entry in a list.

**Reports and CSV exports.** They return files rather than data, and a tool
result that is a download link is a tool result a model cannot read.

**Configuration beyond policies and SSIDs** — RADIUS servers, certificates,
user profiles, VLAN profiles, classification rules. Readable, and the
allow-list would take them, but each is a tool entry every conversation pays
for and none of them answers a question anybody has asked yet.

**Per-port switch state.** There is no endpoint for it. The closest this API
comes is the wired client-health grid, which reports the switch and port each
*client* is on and whether that port is erroring — which `list_client_issues`
returns. A port with nothing plugged into it is not visible at all.
