# Writing a plugin


A plugin is an ordinary Go program:

```go
func main() {
    p := sdk.New("weather", "1.0.0", "Weather", "Reads local weather.")

    sdk.Tool(p, sdk.ToolSpec{
        Name:        "get_forecast",
        Description: "Get the forecast for a city.",
    }, func(ctx context.Context, in ForecastInput) (Forecast, error) {
        return lookup(ctx, in.City)
    })

    sdk.Serve(p)
}
```

Build it into the plugins directory with a manifest and restart:

```bash
go build -o /var/lib/mcpd/plugins/weather/weather ./cmd/weather
echo '{"name":"weather","exec":"weather"}' > /var/lib/mcpd/plugins/weather/plugin.json
```

mcpd starts it once to learn what it is, registers it as a **type**, and mounts
an instance of it named `weather` at `/mcp/weather`. From then on it is
configured like a compiled-in integration: its settings appear on the Plugins
page, an instance with a required setting blank waits rather than mounting, a
saved setting rebuilds the instance without a restart, and a second instance of
the same plugin can be added under another name. `Configured` and
`ConfiguredJSON` in the plugin read what was saved; a setting may be a table
(`sdk.KindCollection`). The manifest's `env` block still works for a plugin
that would rather be configured that way, and is resolved by the host.

The SDK refuses at registration what the host would refuse at mount -- a tool
name outside the verb vocabulary, an unknown capability, a negative rate limit,
a resource without a name -- so the mistake is found at the author's desk
rather than by the whole plugin being skipped. `sdk.ResultBudget` is the
result ceiling, and an error a plugin returns is rewritten by the host the way
an in-tree plugin's is.

See [`examples/echo`](examples/echo) for a complete plugin including an
approval-gated mutation, and the [`sdk`](sdk) package docs for the mutation
contract.

## Naming tools

**`verb_resource`, and never a bare verb or a bare noun.**

The host prefixes every tool with the instance name, so a plugin's `search`
reaches a model as `graylog_search`. That reads as a service and a verb and
says nothing about what is searched — and this plugin can search two different
things. A model choosing between them has only the description to go on, which
is the part it reads second.

`search_messages` and `search_events` make the qualified names
`graylog_search_messages` and `graylog_search_events`, and the answer is in the
name. The [MCP specification](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)
says nothing about naming style beyond uniqueness, but every example in it is
verb-first, and Anthropic's
[tool-writing guidance](https://www.anthropic.com/engineering/writing-tools-for-agents)
is that a namespacing scheme should be chosen and then applied consistently.
This is the scheme.

Keep the verbs to a small set, because the verb carries meaning a model reads
before it reads a description:

| | |
|---|---|
| `list_` | returns a collection; a filter narrows it, a query does not |
| `search_` | returns a collection selected by a query the caller writes |
| `get_` | returns one thing, or one composite answer about one thing |
| a domain verb | when none of the above is honest — `aggregate_messages` computes rather than retrieves |

Two things this rules out, both of which have shipped here before:

- **A bare noun.** `observium_indicators` names a category somebody invented
  and no action at all. What does it do with them?
- **A bare verb.** `graylog_search` was that, and it is worse than it looks: it
  is only unambiguous while the plugin has one searchable thing, so it becomes
  wrong later, silently, when a second is added.

`Title` is the human-readable display name and should mirror the tool's name
rather than restate its category — "Search events and alerts", not "Events".

**Mutations are outside this rule.** A mutation is identified by
`MutationSpec.Action`, which is a `resource.verb` pair — `device.reboot`,
`label.set` — because its first reader is not a model but the approval policy.
That is the string an administrator writes a standing rule against and the
string the audit trail records. Reordering those words would silently stop
stored rules matching, and a rule that quietly stops matching is an
*exclusion* that quietly stops excluding.

**It is enforced.** `plugins.checkToolName` refuses a name outside the
vocabulary at registration — startup for a compiled-in plugin, mount time for
one built with this SDK, and the SDK itself refuses it when the plugin starts,
so an author sees it first. The verbs live in `toolVerbs` in
`internal/plugins/mutation.go`; if you genuinely need a fifth, add it there, in
front of the comment explaining why the set is closed.

A remote MCP server is exempt. Its tools are named by whoever wrote it, and
renaming them would produce names the far end does not answer to — every call
would fail at the last hop with nothing saying why.

The cost of getting this wrong is not a compile error. It is a model choosing
the wrong tool, occasionally, in a way nobody attributes to the name.

## Two instances of one integration

Nothing about a plugin says *which* one it is. Two Observiums pointed at two
networks serve `observium_springfield_list_devices` and
`observium_northgate_list_devices` — the same fourteen tools, the same
descriptions, and a prefix that is a slug somebody typed. A model choosing
between them is reading the name and nothing else.

So every instance carries one host-supplied setting, **What this one covers**,
which a plugin does not declare and cannot know: it is a deployment's fact. It
lands in the two places that reach a model for free:

- the **instructions** a client reads once when it connects, where the purpose
  leads and the plugin's own description follows;
- **each tool's description**, appended as a phrase.

```
Purpose:  the Springfield branch network

instructions →  the Springfield branch network. Reads an Observium install…
tool         →  Lists the devices Observium is monitoring. the Springfield
                branch network.
```

Blank is the default and composes to nothing at all — no phrase, no punctuation,
descriptions exactly as the plugin wrote them. A single instance is already
unambiguous, and a line repeated across fourteen tools to restate the name in
the prefix would be a cost with no question behind it.

It is a phrase rather than a sentence for the same reason: it is paid once per
tool entry, so "the Springfield branch network" buys what "This MCP handles
communications to Springfield" buys, for a third of the tokens.

**Deliberately not a tool.** A `get_description` tool would cost an entry in
every plugin's list whether or not anything called it, and would only help a
model that thought to ask — which is the model that already knew there was an
ambiguity. This arrives before the first call, in what the model is reading
anyway when it decides which tool to use.

The value is read when the plugin is built, so editing it remounts the instance
and the new text reaches a client on its next connection. An assistant halfway
through a conversation keeps the tool list it already fetched.

## Telling people how to use it

A type may declare a `Guide`: three or so questions worth asking an assistant
connected to it, written the way a person would ask them, and the notes that
save a wrong first attempt -- what to configure first, what a name has to look
like, what the integration will not do. The dashboard shows it on the plugin's
page under the address, for the person about to ask their first question. It
is not for developers and it does not list what the tools do: the tool names
already say that.

## A setting that is a table

A plugin's settings are ordinarily scalars: an address, a token, a number. Some
integrations need a *collection* -- the customers one instance serves, each with
an address and a credential -- and a flat key/value store cannot hold that
without synthesising keys, nor can a form edit it in one masked box.

So a field may be `settings.KindCollection`, with `Columns` shaping each row:

```go
{
    Key: "customers", Label: "Customers", Kind: settings.KindCollection, Required: true,
    Columns: []settings.Field{
        {Key: "name", Label: "Business name", Kind: settings.KindString, Required: true},
        {Key: "aliases", Label: "Aliases", Kind: settings.KindList},
        {Key: "host", Label: "Address", Kind: settings.KindString, Required: true},
        {Key: "password", Label: "Password", Kind: settings.KindSecret, Required: true},
    },
}
```

The first column is the row's identity: a required string, unique within the
collection folding case, and what the dashboard lists a row by. Columns are
ordinary fields -- string, secret, list, int, bool, enum -- and may not nest.

What the host does with it:

- rows live in `plugin_rows`, one JSON object per row, with the secret columns
  encrypted as a unit by the same cipher every other stored credential uses;
- the dashboard renders a table with add, edit and remove, each row saved on
  its own through `/api/settings/rows/{key}`. A secret is shown as set or
  missing and a blank on edit means keep, so one row's credential can be
  replaced without any other's being retyped. `PUT /api/settings` refuses the
  key outright;
- the plugin receives the field as `[]map[string]any`, secrets included, and
  decodes it the way it decodes everything else. A `Required` collection with
  no rows leaves the instance waiting to be configured, like a required scalar
  left blank;
- `config.yaml` may supply the rows as a list under the field's key, with a
  secret column given as `<column>_ref`. Rows in the store win outright over the
  file when any exist -- two sources that both contributed rows would leave
  nobody able to say where a customer came from or how to remove it;
- removing the instance removes its rows.

Every row write rebuilds the instance, the way a scalar change does, so a
customer added on the page is reachable without a restart.

What it does *not* do is narrow access. Authorization is per plugin instance,
so a caller who may reach the instance may ask about every row in it. A
collection is for the case where that is right -- one MSP's technicians and
all of that MSP's customers -- and separate instances remain the answer where
it is not. `internal/plugins/threecx` is the first use, and
[3cx.md](3cx.md) describes what it makes of the rows.

## The tool list is a budget

Everything a plugin advertises — names, descriptions, input schemas, output
schemas, annotations — is fetched on connect and enters the model's context
before a single tool is called. It is charged per *conversation*, not per call,
and whether the tools are used or not.

`TestToolList_StaysWithinItsContextBudget` in `internal/app` measures it per
plugin and fails if one grows past its ceiling. The ceilings sit about fifteen
percent above today's cost: the point is to catch a doubling, not to argue
about a sentence. Raising one is fine — do it in a diff somebody reads, and say
why.

Where the cost actually goes, measured across all four integrations:

| | share | |
|---|---|---|
| output schemas | ~41% | derived from your handler's Go return type; nobody writes them |
| input schemas | ~30% | your parameter struct and its `jsonschema` tags |
| descriptions | ~17% | the only part written by hand |
| annotations | ~6% | five fields, fixed |

Two things follow that are worth knowing before you design a plugin.

**Your return type is a context cost.** It is the largest line item and the
least visible one, because no one writes it — a handler returning three nested
collections carries an output schema three times the size of one returning a
flat list. If a result can be flatter, it is cheaper twice over: in the schema
every conversation pays for, and in the result each call returns.

**Grouping endpoints into one tool does not save context.** It reads as though
it must, and this codebase asserted it in three places. Measured, it is false:
`observium` serves three *fewer* tools than `cnmaestro` and costs five
kilobytes *more*, because its grouped tools return composite results whose
schemas grow by about what the extra tool entries would have saved. Cost tracks
total surface area, not tool count.

Group anyway — it is how a model asked one question is kept from choosing
between nine tools that each answer part of it, and that is a good enough
reason on its own. Just do not claim a saving that is not there.

## What one call may return

A tool result is charged twice, and neither charge is visible from the code
that builds it.

**It goes over the wire twice.** The specification says a tool returning
structured content SHOULD also serialize it into a text block, so a client
predating structured content can still recover the payload — and the Go SDK
does exactly that. Your result appears in `structuredContent` and again in
`content[0].text`. That is correct; it is also a 2× multiplier on every size
decision you make.

**The client has its own ceiling.** Claude Code caps a tool response at 25,000
tokens by default. Past it, the response is cut *by the client*, mid-JSON, with
no note saying what went missing — and the model then reasons about a truncated
object it does not know is truncated.

`plugins.MaxResultBytes` is the arithmetic, done once: 25,000 tokens at roughly
3.5 characters each is ~87,500 characters, halved for the duplication, rounded
down to **40,000 bytes** for a plugin to build. Use `plugins.ResultBudget(n)`
where `n` is how many independent collections your result carries — a composite
answer is one tool result, and three collections each bounded at the whole
budget is a result three times past it.

Bound your own results and say so. Truncation will happen on a large estate
whatever you do; the only question is whether the thing doing it can explain
itself and tell the caller what to narrow.

Two things this is not. It is not a substitute for an item limit — `max_items`
bounds how many things come back and this bounds how much, and an estate can
hit either first. And it is not a setting: an operator raising it would only
move where the cutting happens.

## Errors are results, not failures

The specification splits failures in two, and the split decides whether a model
can recover.

A **protocol error** — unknown tool, unparseable arguments — is a JSON-RPC
error. The call did not happen and there is nothing to reason about.

A **tool execution error** — a bad query, an upstream that refused — is an
ordinary result carrying `isError: true` with the message as text. The model
sees it. Returning an ordinary Go `error` from your handler produces exactly
this, so there is nothing to do but write the message well: say what was wrong
*and* what to do instead, because the model is about to act on it. "sort order
is \"sideways\"; it is asc or desc" is a retry; "invalid argument" is a dead end.

`TestToolErrors_ReachTheModelAsRecoverableResults` pins both halves.

## The rule that matters most

If a mutation's `Apply` cannot establish whether its write landed, it must
return `sdk.Indeterminate`. Anything else tells the host the write did not
happen, and permits a retry that applies it twice.
