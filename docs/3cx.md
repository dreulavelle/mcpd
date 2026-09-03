# 3CX

What the 3CX v20 configuration API does that a reader would not expect, and
what this integration does about it. The plugin is `internal/plugins/threecx`;
this is the companion to the comments in it.

One instance serves many phone systems. An MSP with thirty customers has
thirty PBXs, each with its own address and its own system-owner extension; they
are rows of one instance's **Customers** table, so the MSP runs one endpoint,
one tunnel and one ChatGPT connector rather than thirty of each. Every tool
takes a `customer` argument and `list_customers` says what the choices are.

## Customers

The Customers setting is a table (`settings.KindCollection`; see
[plugins.md](plugins.md)). Each row is one business:

| column | |
|---|---|
| Business name | what the assistant is told and answers with; unique within the instance |
| Aliases | other names people use, so "acme" finds "Acme Dental Group" |
| Address | the PBX's FQDN, with or without `https://` |
| System owner extension | the number or email to sign in as; needs the System Owner role |
| Password | that extension's web-client password, stored encrypted |

Rows are added, edited and removed one at a time on the Plugins page, each
saved as it is closed. Replacing one customer's password never means retyping
another's. A file-provisioned host can supply them in `config.yaml` instead,
with the password as a reference so the file holds none:

```yaml
plugins:
  pbx:
    type: threecx
    settings:
      customers:
        - name: Acme Dental Group
          aliases: [acme, ADG]
          host: acme.ny.3cx.us
          extension: "100"
          password_ref: env:ACME_PBX_PASSWORD
```

Rows in the dashboard win outright over the file when any exist.

**Resolution never guesses.** A tool's `customer` is matched against every
name and alias, folding case. An exact match wins. Failing that, a fragment
contained in exactly one customer's name or alias is taken -- "dental" for
"Acme Dental Group" -- but a fragment that fits two customers is refused with
both named, and the refusal tells the model to ask the person which they mean
rather than pick one. No name at all is fine when the instance has one
customer and is refused with the list when it has several. Two rows may not
share a name or alias, or a host, because either would be a call that could
only be resolved by guessing; `Config.Validate` refuses them.

**Access is per instance.** Anyone who can reach the instance -- a key, a
tunnel, a ChatGPT workspace -- can ask about every customer on it. If some
people should see only some customers, those customers go on a second
instance. This is the trade the single connector buys, and it is written on
the setting's own help text so nobody discovers it later.

**Nothing is reached until asked.** Start signs in to no phone system: thirty
customers would be thirty sign-ins per restart, each counted by 3CX's
anti-hacking protection, to mark the plugin degraded over a customer nobody
had asked about. Health is what the last real call to each customer found, a
customer never asked about is not a problem, and `list_customers` with `check`
signs in to every one on demand when somebody wants to know which is wrong.

**Nothing crosses between customers.** Each customer has its own client, its
own token, its own rate limit, its own transport pinned to its own host, and
its own health; there is no cache; and every answer carries a `customer` field
naming the business it is about, so a result can never be read as another
customer's. One customer's PBX being down leaves the others' tools working and
names the failing one on the health report; only every customer failing fails
the plugin's start.

## The tools

Nineteen reads, in eight groups, split by the question a technician is asking:

| | |
|---|---|
| `list_customers` | which businesses this instance serves, their aliases, and whether the last call to each worked |
| `get_system_status` | health, licence, offline trunks, stopped services, disk, backups, and the findings in words |
| `list_services`, `list_active_calls`, `search_events` | which service is down, what is going through now, what the system has logged |
| `list_extensions`, `get_extension`, `list_devices` | who is registered, why one extension behaves as it does, what handsets exist |
| `list_trunks`, `list_inbound_rules`, `list_outbound_rules`, `search_directory` | where numbers come in, where they ring, how calls go out, what a number is |
| `list_ring_groups`, `list_queues`, `list_receptionists` | the things a call can land on that are not a person |
| `get_schedule` | one department's office hours, holidays, time zone and whether somebody forced it open or closed |
| `search_call_history` | did the call happen, and what became of it |
| `aggregate_support_bundle`, `get_support_bundle_report` | the support bundle, read into a digest; a last resort |

Every one is read-only and every one is idempotent.

## Default projections leak credentials

This is the fact the whole integration is shaped around.

`GET /xapi/v1/Users` with no `$select` returns, for every extension,
`AuthID`, `AuthPassword`, `DeskphonePassword`, `VMPIN` and `SIPID` -- the SIP
registration credentials, the handset's web password and the voicemail PIN for
the entire business. `SystemStatus` returns `LicenseKey`. `SipDevices` returns
`PhoneWebPassword` and `ProvLink`. `DeviceInfos` returns `InterfaceLink` and
`Parameters`, which are provisioning URLs, and a provisioning URL is a
credential wearing a different hat: a handset fetches its whole configuration
from it, SIP password included. `Phones` on a user carries
`ProvisioningLinkLocal` and `ProvisioningLinkExt`. A trunk carries
`AuthPassword`, `SeparateAuthId` and `Certificate`.

So the rule here is not "do not return credentials". It is **never fetch
them**, and it is enforced in `transport.go` rather than remembered at each
call site:

- every read must carry a `$select`. A request without one is refused before
  it leaves the process;
- every name in a `$select`, a nested `$select` inside `$expand`, a `$filter`
  and an `$orderby` is checked against `refusedFields` -- the list above -- and
  against a set of fragments (`password`, `secret`, `provision`, `link`,
  `token`, `key`, `pin`) so a field added in a later build is refused by the
  shape of its name rather than waited for;
- every expanded navigation property must carry its own `$select`, because
  `$expand=Phones` without one returns each handset's provisioning link;
- `SipDevices` is not on the allow-list at all. There is no projection of it
  worth having.

A field that is never fetched cannot be logged, cached, returned or exported by
some later mistake. That is the guarantee, and it does not depend on anybody
remembering.

## The read-only guarantee

An allow-list of paths, not a method check. `GET` only would cover the
read-only half; it would not cover `GET /xapi/v1/SipDevices`. So a request is
refused unless its method and its path are both named in `allowed`, and the
one `POST` permitted is the sign-in. Adding a tool that reaches a new endpoint
means naming it there, in front of the comment explaining why the list is
closed.

Redirects are not followed, and a request to any host but the configured one is
refused. Either could carry the bearer token -- a credential for the whole
PBX -- somewhere the operator never named.

## Signing in

There is no API key. The extension's web-client password is exchanged at
`POST /webclient/api/Login/GetAccessToken` for a bearer token:

```json
{"Status":"AuthSuccess","Token":{"access_token":"…","expires_in":3600,"token_type":"Bearer","refresh_token":"…"}}
```

The token lasts an hour and is what travels on every read. The password
crosses the network once an hour and appears nowhere else; the plugin's own
`Config` has it blanked after construction so a dump of the config cannot carry
it.

Three things about the sign-in are worth knowing:

- a wrong password is a **401**. A right password on an extension that needs a
  second factor is a **200** with `Status` not `AuthSuccess` and no token. The
  client tells them apart, because the fix is different;
- 3CX counts failed sign-ins against its anti-hacking protection. The client
  signs in under a mutex, so two cold tool calls arriving together cost one
  sign-in rather than two, and a refused credential is reported rather than
  retried;
- the PBX invalidates every token when it restarts or when the extension's
  password changes. A 401 on a read is answered by one fresh sign-in; a second
  401 is reported as a credential problem.

The extension needs the **System Owner** role. A normal extension signs in
perfectly well and is refused every listing with a 403, so the on-demand check
lists one extension after signing in: a wrong address, a wrong password and a
missing role are then three different sentences rather than one failure inside
the first tool call.

## OData, as 3CX speaks it

`$top` above 100 is a 400 naming the limit. Everything that can return more is
paged at 100 with `$skip`, and `$count=true` is asked for on the first page so
a listing can say how many there are rather than how many it fetched.

`$select` is accepted on every collection and singleton tried, including
`Defs/TimeZones`, `LicenseStatus` and `Groups`. (An earlier integration
recorded `Groups` refusing a `$select` of two properties; on 20.0.9 it does
not.)

`$search` works on `Users` and `EventLogs`. `$filter` with `contains()`, `eq`
on strings and booleans, and `eq` on an enum written as a plain string
(`Type eq 'Error'`, `Type eq 'Queue'`) all work.

**`CallHistoryView` refuses a timestamp comparison.** `SegmentStartTime ge
2026-09-01T00:00:00Z` -- the literal OData specifies, in every spelling tried,
with and without a `cast` -- is a bare 500 with an empty body, while the same
comparison on `EventLogs.TimeGenerated` works. The date functions do work on
the view: `date(SegmentStartTime) ge 2026-09-01`, and `year()`, `month()`,
`day()` and `hour()`. So a time window on call history is pushed to the PBX at
day granularity with `date()` and the exact bound is applied to what comes
back, which means a narrow window on a busy day fetches that day's calls and
returns fewer rows than the limit.

A property that is null is **omitted** from the response rather than sent as
`null`. `TimeZoneId` on a group that has none, `Language` on an extension that
inherits the system's -- neither key appears. Decoding into a struct handles
this; code that ranged over a `map[string]any` and expected every selected key
would not.

## Where the answer actually is

**A trunk has no name.** The name and the host belong to the `Gateway` complex
value it carries. Selecting `Gateway` returns the whole value, which has the
provider's name, host, port and type and nothing secret.

**A department is a `Group`.** Office hours, holidays and the time zone hang
off it, the default one is `IsDefault`, and an extension's department is the
group named by its `PrimaryGroupId` -- or, in full, the first entry in its
`Groups` membership, whose `Rights.RoleName` is the role.

**Forwarding is per status profile, and a profile has one of two shapes.**
`AvailableRoute` -- no answer, busy, not registered, each split internal and
external -- for a profile that means "at my desk"; `AwayRoute` -- internal and
external, each with an "outside office hours too" flag -- for one that means
"not here". Which shape a profile has is a property of the profile, not its
name: on the system this was built against, Available and Custom 1 carry
`AvailableRoute` while Away, Out of office and Custom 2 carry `AwayRoute`. The
tool reads whichever is non-null.

**The key layout is one string of XML** on the extension, `Blfs`:

```xml
<PhoneDevice><BLFS><BLF ID="29" BLFNo="2" BLFType="BLF" BLFTypeID="0">100</BLF>…</BLFS></PhoneDevice>
```

`BLFNo` is the button's position on the phone and can have gaps -- a key left
at its default is not written out. A queue-login key names its mode in `ID`
(`LOGGEDINQUEUE`) and writes a fixed phrase as its value. A custom speed dial
holds the number and its labels one per line.

**A holiday is six integers and two durations.** `Day`/`Month`/`Year` and the
`…End` three for the span; `Year` is zero on one that repeats. `TimeOfStartDate`
is a duration from midnight -- `PT13H30M` is half past one -- and empty covers
the whole day. They are rendered as `2026-12-25` or `--12-25`, and `13:30`.

**Event log lines are templates.** The record reads `Trunk %1$s has changed
status to %2$s` with the values in `Params`. The tool fills them in; a
placeholder with nothing to fill it is left as it was rather than blanked, so
an incomplete line still looks incomplete.

**The trunk count in `SystemStatus` can exceed the `Trunks` collection.** The
system this was built against reports five trunks and lists three; the other
two are bridges that do not appear as `Pbx.Trunk`. The status tool names the
offline trunks it can see and falls back to "3 of 5 registered" for the rest.

## The support bundle

`GET /xapi/v1/SupportInfo` builds and returns the zip the console's "collect
support info" button produces: several hundred files and tens of megabytes,
holding a week of metrics, the event and audit logs as CSV, every service log
and the packet capture where one was taken. It is not in the OData metadata
and it is a file rather than an entity, so it is the one entry on the
transport's allow-list marked `raw`: no `$select` is required of it, and
nothing else may claim that exemption. It is also the one read that costs the
phone system something -- seconds on a small system, minutes of walking its
own logs on a large one -- which is why it is two tools, rate limited to one
capture every ten minutes, and described to the model as the thing to reach
for last.

`aggregate_support_bundle` starts a capture and returns a job at once, because
a tool call cannot wait minutes. The fetch runs detached with a ten-minute
ceiling and a hundred-megabyte cap on the zip, which is held in memory -- a
zip needs random access, and a temporary file of a customer's logs is a thing
to clean up and eventually fail to. `get_support_bundle_report` reports where
the job got to and, once done, the digest; a finished digest is kept an hour
and reused unless `force` is passed.

The digest comes from the `supportinfo` package beside the plugin, ported from
an earlier integration: it reads the dozen files that matter and produces the
system's account of itself, 3CX's own health checks, findings with the lines
they were read from, the event log grouped and judged, call quality where the
media server logged it, the packet capture reduced to RTP streams with loss and
jitter and one-way audio called out, service memory growth, throughput, and the
audit log distilled to the handful of edits that changed a setting. The report
tool returns the summary -- system facts, findings, counts -- and one section in
detail on request, each bounded, with the metric series summarised to peak,
average and minimum rather than listed. The zip itself is never returned.

**A timed packet capture is not in the API.** The console can start and stop a
capture by hand and the bundle then carries the pcap, but the v20 OData
metadata declares no action for it: the only capture-adjacent members are
`DownloadEventLogs`, the report functions and the purge actions. Whatever the
console calls is outside `/xapi/v1`. Offering "capture for three minutes"
would mean finding that endpoint by watching the console, and it would be a
write in the sense that matters -- it starts a process on the PBX -- so it
belongs with the mutations when those come, not on the read side.

## What is cached, and what is never

Nothing. A read tool's result is evidence a model acts on, and on a PBX the
questions are about *now*: is the trunk up, is the handset registered, who is
logged into the queue. A rate limiter (five requests a second by default) keeps
a walk through pages from leaning on a small machine; a cache would trade that
for an answer that had stopped being true. If one is ever added, the department
list and the time zone table are the candidates -- they change when somebody
changes them -- and registration state must never be.

## Testing against a real instance

The unit tests run against a fake PBX that answers the sign-in and a set of
OData paths, and that fails any read reaching it without a `$select`. The
integration tests run against a real one:

```bash
THREECX_TEST_HOST=acme.ny.3cx.us THREECX_TEST_EXTENSION=100 \
THREECX_TEST_PASSWORD=… go test ./internal/plugins/threecx/ -run Integration -v
```

Skipped when the variables are unset. Everything in the OData section above was
found by running them; a stub written from the metadata would have agreed with
the metadata.

To see what the API returns by hand:

```bash
# Sign in. Note the Status field: a second factor is a 200 with no token.
curl -s -X POST https://<pbx>/webclient/api/Login/GetAccessToken \
  -H 'content-type: application/json' \
  -d '{"Username":"100","Password":"…","SecurityCode":""}'

# The schema this version actually serves. swagger.json is not on every build;
# $metadata always is.
curl -s https://<pbx>/xapi/v1/\$metadata -H "authorization: Bearer $TOKEN"

# One extension, with fields named. Never omit $select on Users.
curl -s "https://<pbx>/xapi/v1/Users?\$top=1&\$select=Id,Number,DisplayName" \
  -H "authorization: Bearer $TOKEN"
```

## What is not here, and why

**Writes.** Every one is a change to somebody's production phone system with
their customers' calls on it, and the first version of this integration is
deliberately the half that cannot break anything. When they come they will be
mutations -- planned against live state, diffed for an approver, verified by
re-reading -- and the transport's allow-list will be widened one path at a
time. The paths that would be needed are known: `PATCH Users(id)`,
`POST/DELETE Users`, `PATCH Trunks(id)`, `POST InboundRules`, `POST/DELETE
Holidays`, `PATCH Groups(id)`, and the phone actions under `Users/Pbx.*`.

**Recordings, chat history, contacts.** Call recordings and chat transcripts
are the content of somebody's conversations, and a phonebook is personal data
the business collected for a different purpose. None of them answers a
"phones are down" question, and putting them in front of a model is a decision
an operator should make on purpose, not one an integration should make for
them.

**Reports.** The `Report*` entity sets are parameterised functions
(`ReportCallLogData(periodFrom=…,…)`) that 404 on this build when called the
way the metadata describes. `CallHistoryView` covers the question they are
usually opened for.
