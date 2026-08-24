# The approval policy API

The standing rules that decide which changes are authorised in advance and
which are put to a person. The design and the reasoning are in
[architecture.md](architecture.md#asking-about-everything-is-the-same-mistake);
this is the reference for building against it.

## The rule

```json
{
  "id": "routine-radio",
  "plugin": "cnmaestro",
  "action": "*",
  "principal": "*",
  "max_risk": "low",
  "note": "a channel change is undone by another channel change"
}
```

| field | | |
|---|---|---|
| `id` | required | `^[a-z0-9][a-z0-9_-]{0,63}$`. It is what the audit trail names, so it should read as a reason. |
| `plugin` | `*` | The integration instance, by name. |
| `action` | `*` | The mutation, e.g. `device.reboot`. |
| `principal` | `*` | Whose proposals, e.g. `user:alice@example.com` or `svc:chatgpt`. |
| `max_risk` | `""` | `low`, `medium`, `high`, or empty. **Empty authorises nothing.** |
| `note` | optional | ≤ 256 characters, no control or invisible-formatting characters. Carried into the audit entry. |

An omitted selector is stored as `*`. `critical` is refused as a ceiling.

**Strictly decoded.** An unknown field, an explicit `null`, and an empty-string
selector are all refused, naming the field. This matters because an *omitted*
selector means "anything": without strictness, `{"principle": "svc:agent"}`
would be discarded and the rule would silently cover every principal.

## Resolution

1. A mutation that declares itself irreversible is never authorised. Neither is
   one whose risk level the host does not recognise.
2. **Any matching exclusion wins.** An exclusion is a rule that authorises
   nothing: an empty `max_risk`, or (defensively) a `critical` or unrecognised
   one. If one matches, the answer is "ask a person" — specificity is not
   consulted.
3. Otherwise the matching **grants** are scored — plugin `+4`, action `+2`,
   principal `+1` — and the highest wins. Exactly one rule decides; they are
   never merged. Same-scope duplicates are refused at store time.
4. The winning grant's ceiling is compared against the risk as it finally
   stands.

Exclusion-wins is deliberate and it is not the same as most-specific-wins. An
exclusion is naturally narrow and a grant naturally broad, so scoring them
together handed the argument to the grant:

```
{id: never-reboot, plugin: "*",         action: "device.reboot"}  → score 2
{id: plugin-wide,  plugin: "cnmaestro", action: "*", max_risk: high} → score 4
```

Under scoring, a cnmaestro device reboot auto-approved. It does not now.

**The cost.** An exclusion cannot be granted an exception. "Nobody but Alice
may auto-approve this" is *not* an exclusion plus a narrow grant — the
exclusion wins and Alice is asked too. Write the narrow grant alone: the
absence of a grant already means ask.

## Before you write a rule

**A rule removes an interruption, not the backpressure.** It used to remove
both: the human in the loop was the only thing bounding how fast an agent could
write, and a rule took them out of it. There is now a per-caller limit on
proposing each mutation — `MutationSpec.RateLimit`, one a second unless the
plugin says otherwise, and never absent. A caller past it is refused before
anything is planned or recorded, with a message saying how long to wait.

That is a floor, not a plan. One a second is far above any workflow a person
drives and far below what a model in a retry loop produces, which is the gap it
is sized for — it stops a runaway, and it does not make a badly scoped rule
safe. Scope rules as narrowly as the job allows.

The limit is per caller rather than global on purpose: a single shared budget
would let one runaway agent spend it and leave the operator's own corrective
change refused, and the corrective change is the one that stops the runaway.
What protects the upstream itself is the plugin's own client, which knows what
its API can take.

## Writing rules for a chat client

An agent's own confirmation prompt and a rule do different jobs, and they
compose. The client's prompt asks whether the call may be made at all; the rule
decides whether mcpd needs a person for the change behind it. Where a rule
matches, the propose call is approved and executed before it returns and the
user answers one dialog — their client's. Where none matches they answer two,
and the second is mcpd's, which is the one carrying the impact and the diff.

Neither is a place anyone leaves the conversation. There is no path in mcpd
that requires opening the dashboard to approve a tool call; above the inline
ceiling the assistant shows the change in full and is told explicitly instead,
still in the chat. The dashboard is for history, rules and the audit trail.

So a rule for a chat client is a decision about which changes are routine
enough that the client's prompt is the whole of the interruption:

```json
{ "id": "chatgpt-routine-radio", "principal": "svc:chatgpt",
  "plugin": "cnmaestro", "action": "device.set_radio_channel",
  "max_risk": "low",
  "note": "ChatGPT confirms the call; a channel change is undone by another" }
```

Scope it by `principal` as well as by change. A rule written for one agent's
prompt should not silently authorise a script that has no prompt at all.

Two things the client cannot do for you, so do not write rules as though it
could:

- **Its prompt shows the argument JSON, not the change.** `{"device_id":
  "a3f9c2"}` tells a person nothing. Where the client's dialog is the only one
  a user will see, the mutation's parameters and description are the approval
  UI — name things so the payload reads.
- **Nothing on the wire says the user was asked.** `auto` mode, a user holding
  down Confirm, and a plain API key are indistinguishable to mcpd. A rule is an
  administrator's decision recorded against them; it is not a claim that
  somebody clicked, and `authorized_by_rule` exists so the two are never read
  as the same thing.

## `GET /api/approval-policy`

Capability: `read`.

```json
{
  "rules": [ { "id": "routine-radio", "plugin": "cnmaestro", "action": "*",
               "principal": "*", "max_risk": "low", "note": "..." } ],
  "wildcard": "*",
  "ceilings": ["low", "medium", "high"],
  "default": "Every change is put to a person unless a rule authorises it.",
  "warnings": ["rule \"never-rebooot\" names action \"label.rebooot\", which no mounted plugin registers, so it matches nothing"]
}
```

`warnings` is advisory and may be absent. It names rules whose plugin or action
matches nothing this host currently serves — never a refusal, because a rule
may legitimately name a plugin about to be added. It matters most for an
*exclusion*: a misspelled one authorises nothing, so it looks safe, but it
never fires for the action it was written for and a broader grant decides
instead. Surface these prominently next to the rule.

`rules` is sorted most-specific first. `ceilings` is what the form may offer for
a grant; the empty ceiling is deliberately not in it, because it is a distinct
choice — an exclusion — rather than a level, and the UI should present it that
way. Note that an exclusion beats every grant it overlaps, so the list is not a
priority order.

`409 unreadable_rules` means the stored value does not validate. The host is
asking about everything in that case, which is the same behaviour as having no
rules and a different fact — say which.

## `PUT /api/approval-policy`

Capability: `admin`. Recorded in `settings_history` against the caller.

```json
{ "rules": [ { "id": "routine-radio", "plugin": "cnmaestro", "max_risk": "low" } ] }
```

The whole set is replaced. Partial edits are not offered: whether a rule is
legal depends on the others beside it, so there is no unit smaller than the set
at which "no two rules cover the same thing" can be checked. Send the list you
want to end up with; send `[]` to remove every rule.

Everything is validated before anything is stored, so a bad rule changes
nothing. On success the response is the same shape as `GET`, canonicalised and
carrying any `warnings` the new set produced.

- `400 invalid_rules` — `detail` says which rule and why.
- `409 rules_not_applied` — the write did not land.

Note that `PUT /api/settings` will refuse `approval.auto_approve_rules`: it is
not a form field, and these endpoints are the only way to write it.

## `POST /api/approval-policy/evaluate`

Capability: `read`. Computes over configuration and changes nothing.

```json
{ "plugin": "cnmaestro", "action": "device.reboot",
  "principal": "user:alice@example.com", "risk": "low", "reversible": true }
```

```json
{
  "auto_approve": false,
  "rule": { "id": "never-reboot", "plugin": "cnmaestro",
            "action": "device.reboot", "principal": "*", "max_risk": "" },
  "reason": "rule never-reboot (cnmaestro/device.reboot for *) excludes this from automatic authorisation"
}
```

`rule` is present whenever one decided, including when it is the exclusion that
is the reason a person is being asked. It is absent when nothing matched.
`reason` is prose meant to be shown as-is; do not parse it.

## On an operation

An operation authorised by a rule carries `authorized_by_rule` on
`GET /api/operations` and `GET /api/operations/{id}`, and its `approved_by` is
`system:policy` with `approved_by_name` the same string (it is not an account,
so there is no name to resolve).

**`authorized_by_rule` is the discriminator, not `approved_by`.** A page that
renders the approver field alone will say "approved by system:policy", which
reads as somebody having clicked. When `authorized_by_rule` is non-empty the
row should say a standing rule authorised it and name the rule; when it is
empty, an account approved it and `approved_by_name` is who.

The list endpoint carries only the rule's id. The rule's scope, ceiling and
note are in the `operation.approved` audit entry, which
`GET /api/operations/{id}` already returns alongside the operation — read them
from there rather than from `GET /api/approval-policy`, because the rule may
have been edited or deleted since.

The operation's audit trail carries an `operation.approved` entry whose actor is
`system:policy` and whose detail is:

```json
{
  "reason": "rule routine-radio (cnmaestro/* for *) authorises low changes up to low",
  "execute_by": "2026-08-23T12:15:00Z",
  "channel": "policy",
  "rule": "routine-radio",
  "rule_scope": "cnmaestro/* for *",
  "rule_max_risk": "low",
  "rule_note": "a channel change is undone by another channel change",
  "proposed_by": "user:alice@example.com",
  "asked_a_person": false
}
```

The rule is recorded in full rather than by id alone, so the entry stays true
after the rule is edited or deleted.
