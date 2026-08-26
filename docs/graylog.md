# Graylog

What this integration talks to, and the things about Graylog's API that a
reader would otherwise have to find out by being surprised.

Written against Graylog 7.x. Where a version matters it is named.

Read-only. The guarantee is enforced in `transport.go`, and it is not the same
shape as the one in `observium` — see [The guard is an allow-list](#the-guard-is-an-allow-list).

## The tools

Seven, grouped by the question somebody asks rather than by the endpoint that
answers it. Graylog has hundreds of routes, and a model asked "what does the
log say" should not be choosing between nine tools that each answer part of it.

The grouping is for that clarity, not for context. These seven cost about four
thousand tokens on every conversation whether they are called or not, and
grouping does not reduce that — the composite results carry output schemas
larger by roughly what the extra tool entries would have cost. See
[Tool list budget](plugins.md#the-tool-list-is-a-budget).

| Tool | Answers | Endpoint |
|---|---|---|
| `graylog_search_messages` | "what does the log say" | `POST /api/search/messages` |
| `graylog_aggregate_messages` | "how many", "which is worst", "over time" | `POST /api/search/aggregate` |
| `graylog_search_events` | "is anything wrong now" | `POST /api/events/search` |
| `graylog_list_event_definitions` | "what is being watched", "why was nobody told" | `GET /api/events/definitions` |
| `graylog_list_streams` | "what can I search, and what are these ids" | `GET /api/streams/paginated` |
| `graylog_list_message_fields` | "what can I query on" | `GET`/`POST /api/views/fields` |
| `graylog_get_system_status` | "is Graylog itself well" | `GET /api/system`, `…/indexer/cluster/health`, and on request nodes, inputs, index sets |

## A read is a POST

The three endpoints this integration exists for are POSTs. A question about a
million log lines does not fit in a query string, so Graylog takes it in a
body.

That single fact drives most of what is unusual here. `observium`'s read-only
guarantee is a transport that refuses every method but `GET` — one line,
covering its whole API. The same line here would refuse every call worth
making.

### The guard is an allow-list

So a request is refused unless its **method and path are both named** in
`allowed` in `transport.go`. Everything else is refused before it reaches the
network.

Default-deny is the stronger guarantee, not the weaker one. It is the only
form that can say yes to `POST /api/search/messages` without also saying yes to
`POST /api/system/inputs`, and adding a tool that reaches a new endpoint means
naming that endpoint in front of the comment explaining why.

It also removes the need for the separate deny-list `observium` carries. That
list exists to survive its guard being widened from "GET only" to something
broader. There is nothing equivalent to survive here, because widening this
guard *is* naming a path — and a second list would be a second thing to keep in
step, with the one that drifted being the one nobody was reading.

The prefix the guard strips is the **configured address's own path plus
`/api`**, not a fixed `/api`. A Graylog behind a reverse proxy at `/graylog` is
an ordinary deployment and its requests arrive as
`/graylog/api/search/messages`; a guard that trimmed a constant would leave
that unmatched by every pattern it holds, and the installation would have every
request refused by its own transport with a message pointing at the wrong
thing. What is not under this instance's API root is refused outright rather
than trimmed to something whose tail happens to look familiar.

Two of the patterns carry an identifier and both ends are anchored.
`^/views/fields$` permits the field listing and not `/views/fields/poll`, which
is a POST that triggers a cluster-wide refresh of the field type cache and sits
one path segment away from a read this plugin makes constantly.

## Graylog refuses a POST that does not say who asked

Every non-GET wants an `X-Requested-By` header. It is Graylog's cross-site
guard: a browser cannot set a custom header cross-origin, so requiring one
proves the request came from something that meant to make it.

Without it the API answers `400` with a message naming the header — which reads
as a malformed request rather than a missing one, and sends whoever is
debugging it to look at their query. `errors.go` tells the two 400s apart by
matching on the header name (the wording has changed between versions; the
header name has not) and says outright that a CSRF refusal is a bug in mcpd
rather than anything an operator did.

The header is sent on GETs too. They do not need it, and a header that is only
sometimes present is one somebody eventually forgets to add.

## A token is a username

A Graylog access token is presented as HTTP basic auth with **the token in the
username field and the literal string `token` as the password**:

```
curl -u ccg6i59gk1db4jeqed2di1qetlh5g21423j93esom4q8lelbj42:token \
     -H 'Accept: application/json' https://graylog.example/api/system
```

It looks exactly like a bearer token and is not one. Sending it as
`Authorization: Bearer …` gets a 401 that says nothing about which half was
wrong.

Tokens carry a TTL — 30 days by default — and stop working when they reach it.
From here an expired token and a revoked one are indistinguishable, which is
why the 401 message names both. A username and password are supported for an
installation where a token with a long enough life is not practical; the token
wins when both are set.

## Results are columns, not records

The scripting API answers with a schema and rows of bare values:

```json
{"schema":[{"column_type":"field","field":"source"},
           {"column_type":"metric","function":"avg","field":"took_ms",
            "name":"metric: avg(took_ms)"}],
 "datarows":[["a.example",84.6],["b.example",96.0]],
 "metadata":{"effective_timerange":{"from":"…","to":"…","type":"absolute"}}}
```

This integration keeps that shape all the way out to the model. Fifty rows of
`{"timestamp":…,"source":…,"message":…}` repeats every field name fifty times,
which is the same information for several times the context — and context is
the budget a tool result actually spends.

The tool descriptions say the rows are positional, because a model handed
columns it was not told about will read the first row as a header.

One wrinkle: a grouping column is named by its field, and a metric column is
not. Two metrics over the same field are different columns, so the field alone
would name them both the same thing. `columnNames` uses Graylog's own label for
a metric and strips the `metric: ` it prefixes.

## A query with no time range reads the whole cluster

Graylog will happily scan every index a token can see, synchronously, on
somebody's production log cluster. So:

- Every searching tool sends a time range whether the caller named one or not.
  The default is `default_range_seconds`, fifteen minutes out of the box — which
  is the answer to "what is happening", the question somebody is asking when
  they do not name a window.
- **A relative range of zero means every message Graylog holds.** "The caller
  named nothing" must therefore never reach the API as a zero, and
  `TestResolve_NeverSendsAnUnboundedRange` is what says so.
- `max_range_seconds` is an optional hard ceiling on how far back any search
  reaches. Zero, the default, is no ceiling: reviewing an incident from last
  month is the second most common thing this integration is for, and refusing
  it by default would be guessing at a policy nobody stated.

The ceiling is about **age, not width**. An operator who caps searches at seven
days means "not older than seven days", so a one-minute window a year ago is
exactly what they meant to refuse.

### Three ways to name a window, and only one at a time

`range_seconds`, or `from` and `to`, or `keyword`. More than one is refused
rather than silently having one ignored — the whole point of a time range is
that somebody knows which window they are looking at.

An exact window needs **both** ends. A half-open window is not a smaller ask,
it is an ambiguous one: "from Tuesday" could mean until now or until the end of
Tuesday, and the two differ by days of indices.

A `keyword` window ("yesterday", "last 2 hours") is parsed by Graylog rather
than here, so it is **refused whenever `max_range_seconds` is set**. A ceiling
with one way around it is not a ceiling, and enforcing it everywhere except one
place would be worse than saying so.

Every result carries `window_searched`, taken from the `effective_timerange`
Graylog reports rather than from what was asked for. Only Graylog knows what a
keyword resolved to, and a count without the window it covers is a number with
no unit.

## Listings come in three shapes

Graylog answers a listing as a bare array, as an object with the collection
under a name of its own (`{"total":2,"streams":[…]}`), or as a generic
paginated envelope whose list key is chosen per endpoint. `pickList` names the
candidates it will accept.

An absent key is an **error**, and the message names the keys the response
actually carried. This is `observium`'s lesson borrowed whole: there, a wrong
envelope key returned no items and no error, and the tool above reported that
there were none — seven of twelve routes were wrong and nobody could tell.

`/api/streams/paginated` is used rather than `/api/streams`, which is marked
deprecated in the server source. If the paginated envelope's list key turns out
to differ from `streams`, `pickList` will say so by name rather than reporting
an installation with no streams.

## Inputs carry credentials

`GET /api/system/inputs` returns each input's `attributes`, and those are
whatever the input's plugin declares: a syslog input has a port, an AWS input
has a secret access key, a Beats input has a TLS key password. Graylog hands
the whole map to any account that can read inputs.

`safeInputSettings` is an **allow-list** of the attribute names this
integration will return, and it is short. A deny-list would only ever cover the
names somebody thought of, and the attribute set is open-ended by design. What
is on it is what somebody actually asks about an input — where it listens and
whether it is encrypted.

A tool result naming a credential is a live credential in a model's context,
and from there in whatever the transcript reaches. The result says outright
that settings were withheld rather than quietly returning a short map.

## What is cached, and what is never

One class, `cache_seconds`, five minutes by default: how the installation is
*arranged*. Streams, alert rules, field names, inputs, index sets.

Never cached, and the omissions are the point:

- `/search/messages` and `/search/aggregate`. A search is a question about now.
  Answering "no errors in the last fifteen minutes" from a copy made five
  minutes ago is the worst answer this integration can give, because it is
  indistinguishable from a true one.
- `/events/search`. Same reason, more sharply: this is the alert list.
- `/system`, `/cluster`, `/system/cluster/nodes`,
  `/system/indexer/cluster/health`, `/system/notifications`. These are read
  precisely when somebody suspects the installation is unwell, which is the
  moment a held answer is wrong. `/system` is on the list twice over: it is
  also the startup probe, and a liveness check answered from memory is not one.

The rule is an allow-list, so an endpoint `cacheTTL` does not recognise is
fetched every time and a tool added later cannot quietly start being served
from memory. Nothing stale is ever served: the reader is a model about to act
on what it is told, and "this is what Graylog looked like a while ago" is not a
safer answer than waiting.

`observium` splits its cache into two classes because it has two clocks — what
a poller writes, and what an operator arranges. Graylog's equivalent of the
first is the log data itself, and none of that is cacheable at all, so one
class is all that is left.

## Two ceilings on an answer

Neither is a setting. They exist for the pathological row rather than for
tuning; an operator who wants a smaller answer has `max_messages`, which is the
knob that means what they mean.

- `maxFieldChars` (8000) bounds one value. A single log line can be a megabyte
  of stack trace. It is cut rather than dropped — the first few thousand
  characters are the ones somebody wants — on a rune boundary, and the result
  says how many values were cut.
- `maxResultChars` bounds the whole answer across every row. It is the backstop
  for the case `max_messages` cannot see: fifty ordinary-looking messages that
  happen to be large. It never drops the *first* row, because an answer of
  nothing at all — because the one matching message was large — is worse than
  an answer of one large message.

  The number is `plugins.MaxResultBytes`, not one this package chose. It was
  120,000 here, picked by eye, which is about 30,000 tokens — and because every
  result is sent twice (structured content plus a serialized text block, which
  the specification asks for), roughly 60,000 tokens on the wire against a
  client that stops at 25,000. A search that large was going to be cut by the
  client, mid-JSON, with nothing saying what went missing. See
  [What one call may return](plugins.md#what-one-call-may-return).

## An empty result is not an all-clear

Three places where the honest answer needs a sentence beside it, because the
alternative is a model reporting that everything is fine:

- **No events in the window.** A condition nobody wrote a rule for raises no
  event. The note points at `graylog_list_event_definitions`.
- **A query naming a field that does not exist.** It matches nothing and
  reports no error. `graylog_list_message_fields` exists for this, and an empty field search
  says why it matters.
- **A stopped stream.** It still holds what it collected before it was stopped,
  so searching one returns old messages and no new ones. `graylog_list_streams`
  counts how many are stopped.

`graylog_list_event_definitions` also counts two things rather than leaving them to
be derived from the rows: rules that are disabled, and rules that are enabled
with no notification attached. The second is the shape "why did nobody get
told" usually turns out to have, and a model summarising twenty rules will not
reliably notice either.

## Search backend health, and what yellow costs

`GET /api/system/indexer/cluster/health` reports the search backend's cluster
health — OpenSearch or Elasticsearch depending on the deployment. It is called
"search backend" here rather than by product because which one is behind a
given installation is not something this integration knows, and a field called
`elasticsearch` on an OpenSearch cluster is a small lie that costs somebody
five minutes.

Each colour is returned with what it costs, because the colours are widely
recognised and widely misread. **Yellow** in particular reads as "degraded,
some data unavailable" and is not: it is the ordinary, permanent steady state
of a single-node cluster with replicas configured, and treating it as an
incident sends somebody looking for a fault that is a setting.

**Red** is the one that matters to a reader of search results: at least one
primary shard is unassigned, so searches over that data are silently
incomplete.

## Logging

Nothing that leaves this process carries log content.

`Client.do` writes one debug line per upstream call — method, path, status,
byte count, duration — and never the body or the query. A successful body is
somebody's log data; a query names their fields and hostnames. Both are exactly
what an integration reading a log platform must not spill into a log file.
`TestClient_NeverLogsABodyOrAQuery` is the test that holds it.

Error messages do quote failure bodies, because a Graylog rejecting a query
says which part it could not parse and that message is the entire fix. URLs are
redacted of credentials and query strings before they reach a log or an error a
model reads back.

## Testing against a real installation

Point the settings at an instance, restart, and read the startup line — it
names the version, which is the single most useful thing to have when an
endpoint answers 404 later.

```bash
# The probe this plugin makes at startup, by hand.
curl -u "$TOKEN:token" -H 'Accept: application/json' \
     https://graylog.example/api/system

# The search, as the plugin sends it.
curl -u "$TOKEN:token" -X POST \
     -H 'Content-Type: application/json' -H 'Accept: application/json' \
     -H 'X-Requested-By: mcpd' \
     -d '{"query":"","fields":["timestamp","source","message"],"size":5,
          "timerange":{"type":"relative","range":900}}' \
     https://graylog.example/api/search/messages
```

Dropping the `X-Requested-By` from the second one is the quickest way to see
the 400 that `errors.go` exists to explain.

## What is not implemented, and why

**Anything that writes.** Pausing a stream, starting an input, enabling an
alert rule and setting a maintenance-style mute are all reversible and
re-readable, which makes them plausible first mutations. They are deliberately
absent from a first version: adding one means widening `allowed` and declaring
a `MutationSpec`, and that is the amount of friction the decision deserves
against a production log cluster.

**One message by id.** `GET /api/messages/{index}/{id}` returns a whole message
record, but reaching it needs the index name, which a search result does not
carry unless it is asked for. Passing more field names to `graylog_search_messages`
covers the same ground for now.

**Dashboards, views and saved searches.** They render Graylog's own data for a
person looking at Graylog. Nothing a model does with them helps decide which
MCPs run, who can reach them, or what happened — see the test in
[architecture.md](architecture.md#the-load-bearing-decisions).

**Lookup tables, pipelines and extractors.** Reading them would say how
messages are transformed on the way in, which is a real question. It is a
second tool group and nobody has asked for it yet.
