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

**Resolution.** Every matching rule is scored — plugin `+4`, action `+2`,
principal `+1` — and the highest score decides. Exactly one rule decides; the
rest are ignored rather than merged. Two rules with the same scope are refused
when the set is stored.

**What no rule can do.** Authorise a mutation that declares itself
irreversible; authorise a risk level the host does not recognise; authorise
`critical`.

## `GET /api/approval-policy`

Capability: `read`.

```json
{
  "rules": [ { "id": "routine-radio", "plugin": "cnmaestro", "action": "*",
               "principal": "*", "max_risk": "low", "note": "..." } ],
  "wildcard": "*",
  "ceilings": ["low", "medium", "high"],
  "default": "Every change is put to a person unless a rule authorises it."
}
```

`rules` is sorted most-specific first, which is the order resolution considers
them in. `ceilings` is what the form may offer; the empty ceiling is
deliberately not in it, because it is a distinct choice ("always ask") rather
than a level.

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
nothing. On success the response is the same shape as `GET`, canonicalised.

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
  "reason": "rule never-reboot authorises up to nothing and this change is low"
}
```

`rule` is present whenever one matched, including when the matching rule is the
reason a person is being asked. It is absent when nothing matched.

## On an operation

An operation authorised by a rule carries `authorized_by_rule` on
`GET /api/operations` and `GET /api/operations/{id}`, and its `approved_by` is
`system:policy`. Render the two differently: "approved by system:policy" reads
as somebody having clicked, and nobody did.

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
