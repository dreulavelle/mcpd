# Architecture

How mcpd is put together, and why. This is the developer document; the
[README](../README.md) covers running it.

## Shape

One binary, two listeners, one database.

```
ChatGPT ──── Secure MCP Tunnel (outbound) ────┐
                                              ▼
browser ──── :8081 dashboard ──────────────► mcpd ──► SQLite
scripts ──── :8080 /mcp/{plugin} ─────────►         └► plugins
```

The MCP listener and the dashboard are separate ports on purpose. They have
different audiences and different exposure, and a firewall rule can only tell
them apart if they are separate listeners.

The tunnel runs inside the process and reaches the MCP server over an in-memory
transport, so there is no socket for it and no second credential. mcpd never
needs an inbound port, public DNS, or a NAT rule.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/mcpd` | Entry point, flags, `-check`, `init` |
| `internal/app` | Wiring and lifecycle: builds everything, owns shutdown |
| `internal/mcp` | The MCP host — routing, authentication, per-plugin endpoints |
| `internal/admin` | Dashboard JSON API and the embedded SPA |
| `internal/operations` | The approval engine: state machine, policy, executor |
| `internal/plugins` | Plugin registry, tool attachment, approval tools |
| `internal/auth` | Principals, roles, capabilities, static tokens |
| `internal/auth/users` | Accounts, passwords, browser sessions, registration |
| `internal/observability` | Logging, redaction, health, metrics, and the copy of the log the dashboard streams |
| `internal/auth/sso` | Signing in through Google, GitHub, Entra, or the operator's own provider |
| `internal/auth/groups` | Groups, membership, and the one union that decides reach |
| `internal/auth/apikeys` | Bearer credentials this host issued, and the verifier over them |
| `internal/storage/sqlite` | Schema, migrations, every transaction |
| `internal/tunnel` | The embedded OpenAI tunnel client, one per connector |
| `internal/settings` | Runtime configuration in the database |
| `internal/mcpservers` | server.json, and the snapshot of a remote server's tools, with its age |
| `internal/plugins/mcpremote` | Mounting a remote MCP server as a plugin |
| `internal/registry` | Browsing the public catalogues of MCP servers |
| `internal/messaging` | In-process bus and outbox publisher |
| `internal/cachestore` | The bounded, timed map every cache is built on |
| `internal/servertls` | Self-signed CA and certificate issuance |
| `internal/config` | The four keys that stay in a file, and the one-time import of the ones that did not |
| `sdk` | Building out-of-process plugins |

## The load-bearing decisions

**What belongs here is settled by a test, not by taste.** mcpd is a control
plane for MCP servers, and the test for any feature is: does it help somebody
decide *which MCPs run, who can reach them, and what happened?* Rendering an
integration's own data — a device topology, a metrics dashboard — fails it.
Plugin tools return data to the model, not to mcpd's UI.

This is written down because its absence let a bad idea get as far as a
proposal. The idea was plausible, it was about a real integration, and nothing
in this document said no to it — which is what a missing test looks like from
the inside.

**SQLite is the only authority.** Whether a change was approved is answered by
a row, never by a message. Every executor reloads and revalidates before
acting, so a lost, duplicated, or forged event costs latency at worst.

This is also why there is no broker. On one node a message bus carries a
wake-up signal from the process to itself, which an in-process channel does for
free. `internal/messaging` is that channel plus a transactional outbox; nothing
about the design assumes it stays in-process, and the outbox is what makes
swapping it a configuration change rather than a rewrite.

**Nothing writes without an approval it can prove.** A mutation's payload is
hashed at proposal and frozen. Before execution the hash is recomputed and
compared inside the same statement that claims the operation, so a tampered
payload cannot execute even if it slipped past every check above it.

**A claim of verification has to be earned.** Three proofs make a change a
*reviewed change*: the approver saw the exact fields, drift between proposal
and execution is detectable, and the outcome is confirmed by re-reading the
target. A mutation declares whether it can offer the third (`Verifiable`) and
supplies the second by declaring preconditions. Anything short of all three is
a *gated call* — it was authorised and it happened, and that is the whole of
the evidence. ("Authorised" rather than "a human said yes": a standing rule can
authorise a change with nobody being asked. Who authorised it is a separate
fact from what can be proved about it, and the two are kept apart.) The two words are kept apart in the note the model reads and
in what the API returns, because the second borrowing the first's credibility
is exactly how a system ends up claiming more integrity than it has.

An operation that cannot be verified settles with `outcome_verified` null, not
false. "Not checked" and "checked and did not match" are different facts, and
collapsing them is what let the executor report a confirmation it never made.

**Proposing and approving are separate acts.** A model can describe a change;
it cannot authorise one. Approval references a stored operation by id and
cannot carry parameters, so the thing approved is exactly the thing reviewed.

**An ambiguous outcome is recorded as ambiguous.** If the process dies between
issuing an upstream write and recording its result, the operation lands in
`indeterminate`, not `failed`. Calling it a failure invites a retry, and the
retry double-applies the change.

**Access is per plugin.** A credential lists the plugins it may reach.
Everything else returns 404 rather than 403, so an agent scoped to one
integration cannot discover which others are deployed.

## The mutation state machine

```
                  ┌─────────────► rejected
                  │
draft ──► pending_approval ──► approved ──► executing ──┬──► succeeded
                  │                  │                  ├──► failed
                  │                  │                  └──► indeterminate
                  ├──► expired       └──► expired
                  └──► cancelled
```

Terminal: `succeeded`, `failed`, `rejected`, `expired`, `cancelled`.

`indeterminate` is deliberately **not** terminal. It means execution began and
the outcome is unknown, which is resolvable by observation rather than final.
Anything that treats it as settled — a retry, a re-proposal of the same intent
— risks applying a change that already landed.

Each transition is guarded in SQL. The claim into `executing` carries three
conditions in its `WHERE` clause: still approved, payload hash unchanged,
approval not expired. Anything else matches zero rows and the caller learns it
lost the race. That is what makes execution at-most-once.

## Approval, and where the human is

**Nobody is ever sent to the dashboard to approve a tool call.** Every path
ends in the conversation the change was asked for in. An approval that costs a
context switch is one people arrange not to need, and the arrangement they
reach for is a rule broader than the one they meant to write. The dashboard is
where history is read, rules are written and the audit trail lives; it is not a
step in this flow.

So mcpd asks in the conversation: the MCP specification's elicitation lets a
server put a question to the user through the client, and the answer returns as
a real user action rather than a model decision. `internal/plugins/elicit.go`
raises it; `ApproveInline` records it. What that question carries is the point
of it — impact prose, the field-level diff, and what *cannot* be proved — where
a client's own dialog can only show the tool name and the argument JSON it was
called with.

The inline ceiling, `approval.inline_max_risk`, is a setting, and it is a
ceiling on the *shortcut* rather than on where the decision happens. Above it
the yes/no prompt is withheld and the assistant must show the change in full
and be told explicitly before calling the approve tool. Still in the
conversation; just no longer settled by one word.

Enforcement is the same row either way. A client that cannot elicit gets the
two-step flow rather than an unguarded write; there is no path that skips it.

### What the client decides, and what it cannot

Tool annotations — `readOnlyHint`, `destructiveHint`, `openWorldHint` — tell a
client how to frame the call. That decides whether a person is *shown* a
change; it never decides whether the change may happen. ChatGPT confirms write
actions by default and shows the raw argument JSON with a Confirm/Deny, Codex
takes four modes (`auto`, `prompt`, `writes`, `approve`), and the Responses API
has `require_approval` with `mcp_approval_request`/`mcp_approval_response`
items. All of it is client-side framing: there is no protocol slot a server can
fill with a rendered diff, no callback saying a user was asked, and nothing
that survives into a record. A server that trusted "the client probably
prompted" would be recording a decision it has no evidence anyone made.

Because the hints are the only lever, they must be accurate about what the call
*can* do rather than what it usually does. `destructiveHint` follows
`MutationSpec.Reversible`: a change that can be undone is not destructive, one
that declares no way back is exactly what a client should put in front of
somebody. It was hardcoded false on the premise that proposing "changes nothing
upstream" — true until standing rules arrived, and wrong from then on in
precisely the case where nobody is asked.

### One prompt, or two

A propose call under a matching standing rule is approved and executed before
it returns, and the response carries the outcome. The user sees exactly one
dialog: their client's own. That is the intended shape for routine work, and
the operation still records the diff, the drift check, the verification and the
rule that authorised it.

Without a rule the user answers twice — once to let the call through, once to
decide the change — and the second dialog is the one carrying anything worth
reading. That is the price of a change nobody authorised in advance, and it is
the right price. The way to stop paying it is a rule scoped as narrowly as the
job allows, not a hint that says less than the truth.

## Asking about everything is the same mistake

Risk was computed on every proposal, stored, displayed, and consulted by
nothing. Every mutation was equally consequential as far as the gate was
concerned, which means a change of nothing much interrupts a person exactly as
hard as rebooting a site does — and a gate that inconvenient is one people
route around. Wanting an agent to be able to nudge a channel without a human
in the loop is not wanting less safety; it is wanting the interruption to be
worth something when it happens.

So a **standing rule** can authorise a class of change in advance.
`internal/operations/autoapprove.go` is the whole of it: three selectors, a
ceiling, and a note.

| | |
|---|---|
| `plugin` | which integration, or `*` |
| `action` | which mutation, or `*` |
| `principal` | whose proposals, or `*` |
| `max_risk` | the highest risk it authorises; **empty authorises nothing** |

**Any matching exclusion wins; otherwise the most specific grant does, and only
one rule ever decides.** An exclusion is a rule that authorises nothing — an
empty ceiling — and it is checked before specificity is consulted at all.
Grants are then scored, plugin `+4`, action `+2`, principal `+1`, and the
highest takes it. Two rules on one scope are refused when the set is stored,
because picking between them would be right only by accident.

Exclusion-wins is not the same as most-specific-wins, and it replaced it. An
exclusion is naturally narrow and a grant naturally broad, so scoring the two
together handed the argument to the grant: `never-reboot` on `*/device.reboot`
scores 2 and a `cnmaestro/*` grant scores 4, and a cnmaestro device reboot
auto-approved. It does not now. The cost is that an exclusion cannot be granted
an exception — "nobody but Alice may auto-approve this" is the narrow grant
alone, because the absence of a grant already means ask.
[`approval-policy.md`](approval-policy.md) is the reference.

**The default is to ask about everything.** The zero value of the policy is no
rules, and no rules means every mutation goes to a person exactly as it did
before this existed. A policy that loosens on upgrade is the wrong direction to
be wrong in, so nothing changes until an administrator writes a rule. Rules
have no fallback in the configuration file for the same reason: the one setting
whose effect is to skip a human belongs where the change is recorded against
whoever made it, and where the dashboard can show the whole set.

**Three things no rule may authorise.** A mutation that declares no way back
(`MutationSpec.Reversible`), because the case for a standing authorisation is
that a mistake is cheap to correct and it does not survive the absence of a
correction. An unrecognised risk classification, because an unknown is exactly
the thing to put in front of someone. And `critical` — a level an operator can
quietly opt out of is not a level.

**Risk is raised before the policy sees it.** The ceiling is compared against
the risk as it finally stands: the mutation's declaration, raised by the plan
for these specific parameters, raised again by any operator override. A plan
that reclassifies a change upward puts it back in front of a person even though
the proposal qualified without it. The same re-plan runs immediately before
execution, and where a *rule* authorised the change and the re-plan raises the
risk, the executor refuses (`RISK_RAISED`) — the rule authorised a change of
one severity and the target now says it is another, and nobody ever looked.
Where a *person* approved, the same raise changes nothing: they saw this change
and said yes to it, and treating a reclassification as a withdrawal would make
every approval provisional.

**What you lose is the interruption, not the evidence.** An auto-approved
operation is an ordinary operation. The row is written, the payload frozen and
hashed, plan/apply/observe runs, drift is checked, the outcome verified where
the mutation can prove one, and every transition is in the hash-chained trail.
It skips the ask and nothing else. So the property this rests on survives with
one word changed: nothing writes without a **recorded authorisation** it can
prove.

Which is why the record has to name the rule. `operations.authorized_by_rule`
holds it, written in the same guarded `UPDATE` as the approval and immutable
afterwards, and the audit entry carries the rule's scope, ceiling and note in
full — a rule can be edited or deleted, and an entry naming an identifier whose
meaning has since changed would describe an authorisation that never happened.
The approver is `system:policy`, never the principal who proposed: attributing
it to them would say a person approved their own write, which is the one thing
that did not happen. "Auto-approved" with nothing naming the rule is exactly
the unprovable approval this project exists to avoid.

**Assurance is orthogonal and stays orthogonal.** *Nobody was asked* and
*nothing can be proved* are different facts. An auto-approved change that
declares preconditions and can be re-read is still a `reviewed_change`; one
that cannot is still a `gated_call`. Collapsing the two would let the vocabulary
that exists to stop a claim being overstated start overstating one.

**Auto-approval does not consult `CapApprove`.** The authority is an
administrator's rule, not the proposer's standing; what bounds the proposer is
`CapPropose`, checked where every proposal is. Writing rules is `CapAdmin`;
reading them is `CapRead`, because "why was I not asked" is a question an
operator has to be able to answer.

**A rule removes an interruption, and something else has to take over the
backpressure.** Before a rule existed, a runaway agent could only pile up
proposals somebody would decline; under one it lands writes at whatever rate it
can call. The human in the loop was doing that job as a side effect, and a rule
is a decision to stop paying for it.

So a mutation now carries a rate limit of its own, and unlike a read tool's it
is never absent: `MutationSpec.RateLimit` defaults to one proposal a second and
a plugin may raise or lower it knowing what its own upstream costs. Unbounded is
not a defensible zero value for a write. A read tool that declares no limit
costs an upstream a request; a mutation that declares none costs it a *change*,
and under a rule nobody is asked first.

**The limit is per caller; the read tool's is global.** The difference is not an
inconsistency. A read tool's limit protects an upstream's quota, which is a
shared resource no caller has a claim on. A mutation's limit exists because one
agent can loop, and a single global budget would let that agent spend it and
leave the operator's own corrective change refused — and the corrective change
is the one that stops the runaway. What protects the target from many callers at
once is where it has always been: the plugin's client, which knows what its API
can take.

**A refusal costs nothing a retry would find spent.** It is checked after the
authorization gate and before everything else — before the plan, which reads
upstream, and before the operation is recorded. So a refused proposal leaves no
row, spends no idempotency key, and makes no upstream call. That matters more
here than for a read: a refusal that consumed the idempotency of the operation
it refused would make the retry the caller was told to make return the wrong
answer.

**A rule is decoded strictly, and a misspelled selector is an error.** An
omitted selector means "anything", which is the convenience that makes
strictness load-bearing: `{"principle": "svc:agent"}` would otherwise be
discarded silently and the real principal default to every principal, turning a
deliberately narrow rule into a global one with nothing saying so. An unknown
field, an explicit `null`, and an empty selector are all refused, and the check
lives on the rule type rather than in the handler — a `json.Decoder`'s
`DisallowUnknownFields` does not reach inside a custom `UnmarshalJSON`, so the
type is the only place that covers the API, the settings store at startup, and
a restore alike.

Rules are read and written at `GET`/`PUT /api/approval-policy`, and
`POST /api/approval-policy/evaluate` answers "which rule would apply, and why"
before a change is proposed rather than only afterwards from the record. The
shapes are in [approval-policy.md](approval-policy.md).

## Identity

Two kinds of caller, and they are not modes that exclude each other.

**People** sign in to the dashboard with an email and password, and hold a
session in an HttpOnly cookie the page cannot read. Writes carry a CSRF token
in a header: a cookie travels on a request another site caused, a header
cannot be set cross-origin, and that difference is the whole defence. Rights
are re-read per request, so disabling an account ends it immediately.

The first account is created from the dashboard. An instance with none offers
to create one and the registrant becomes administrator; the emptiness check
runs inside the write transaction, so two browsers racing an unclaimed instance
produce one administrator and one refusal.

**Machines** present a bearer token: one declared in `auth.static_tokens`, or
one an administrator created in the dashboard. Both are the same kind of thing
and go through the same authorization; see *Groups, and keys this host issued*.
The tunnel presents nothing — it authenticates to OpenAI's control plane with a
runtime key and carries its identity from configuration.

Roles are `user` and `admin`. A user reads, proposes, and approves; an admin
additionally changes settings, makes tunnels, manages accounts, issues keys,
and clears history. Capabilities (`read`, `propose`, `approve`, `admin`) are
what code checks — never the role directly. What a caller may *reach* is a
separate axis and is decided by groups.

**A display name is a rendering, never an identity.** An account is identified
by its address, and that is what every audit record, every guard and every
grant is keyed on. The name is optional, falls back to the address when empty
so nothing renders blank, and is resolved when a page is drawn rather than
stored beside the thing it describes — a record keyed on a value its own
subject can edit would be a record of nothing.

The rule is enforced twice, and the second time is not belt-and-braces. It is
checked on the way in, and checked again by the one function every render goes
through, because the column is older than any rule about what may go in it and
a database may hold a name written when nothing was checked. A value the rules
now refuse renders as the address instead. The schema cannot cover this and it
would be dishonest to pretend otherwise: a `CHECK` can express the length, and
enumerating the format characters in SQL would catch a score of the hundred and
seventy in the category and drift from the Go rule the first time either
changed. Migration `0011` therefore normalises the length retroactively and
nothing else, and re-checking on read is what makes that sufficient. The stored
value is left alone rather than corrected, so its owner can see what is there
and replace it.

Because identity does not depend on it, an account may set its own name without
`admin`. `PATCH /api/account` carries no identifier and can only ever edit the
account the request authenticated as, so there is no check to get wrong;
naming somebody else is still `PATCH /api/users/{id}` and still `admin`. What
bounds the value is a length, a refusal of control and invisible-formatting
characters — a newline breaks a log line in two, and a bidirectional override
renders a name as something it is not — and a condition in the `WHERE` clause
of the write refusing a name that is another account's address.

## Signing in with somebody else's identity provider

Google, GitHub, Microsoft Entra and whatever OpenID Connect provider the
operator runs themselves can sign a person in. `internal/auth/sso` runs the
flow; `internal/auth/users` decides what it means.

The fourth of those is one provider, not a family. `auth.oidc.issuer` names a
single address; an identity is a subject at that issuer, and pointing the
setting somewhere else is therefore not a reconfiguration but a different set
of people. Only the issuer differs from Google in code — the same discovery,
the same PKCE, nonce, single-use state and pinned redirect — because the parts
that must not vary by provider are the parts kept in one copy.

**The issuer is checked by one rule, in `settings`.** It decides where a client
secret is sent and whose signature is believed for an identity, so it is
refused unless it is https (or loopback), carries no query or fragment, and is
not the `.well-known` address somebody pasted by mistake. The sign-in flow
calls the same function the settings form does: a rule written twice is a rule
that becomes two rules.

**A self-hosted provider is held to `email_verified` like Google, not excused
like Entra.** Entra's exemption is bought by the tenant — this host refuses to
run it without one directory, so the address in a token it accepts was assigned
by an administrator. An arbitrary issuer buys nothing equivalent: it may have
open self-registration, in which case an unverified address is an
account-takeover path into an existing mcpd account with the same address. An
operator whose provider reports everyone as unverified should turn on email
verification there rather than have mcpd stop looking.

**An unlinked provider identity is not an account, whatever address it
carries.** This is the decision the whole feature is shaped around. A Google
sign-in for `alice@corp.com` arriving at a host that already has a password
account for `alice@corp.com` does **not** sign in as that account. It is
refused, and the refusal says what to do instead: sign in with the password and
link the provider from the profile page. Adopting the account on the strength of
the address would hand it to whoever controls that address at the provider —
and addresses get recycled, domains lapse and get re-registered, and a personal
account can be created for a company someone no longer works at. What the
provider proves is control of an address; what has to be proved is ownership of
the mcpd account, and only signing in here proves that. So linking is an act by
the already-signed-in account and it writes a row in `user_identities`, which is
the only thing that ever turns a subject into an account.

The key is the provider's subject, never the address. A `sub` is immutable and
a login or an email is not — GitHub releases a login when it is changed, and
the next person to take it would come to own the account.

**SSO cannot claim an unclaimed instance.** `CreateFirst` is what makes somebody
this host's administrator, and completing a flow at a third party is not a claim
mcpd can honour: a fresh host anyone can reach would belong to whoever got there
first with a Google account. `Register` refuses outright when no account exists,
checked inside the write transaction like every other guard here. The setup form
is password-only for the same reason.

**Pending is not disabled.** `disabled` is a decision an administrator made
about an account; a registration awaiting approval is an account nobody has
decided about, and the two need different columns and different words. A pending
account may **authenticate** — that is how it proves who it is, and it is what
lets it be shown a screen saying it is waiting — and holds no capability at all.
That is settled on the principal: `Principal.Pending` makes `Can` return false
for everything, so the dashboard API, the MCP endpoint, every tool call and
anything added later refuse it without having been told pending accounts exist.
The console's waiting screen is a courtesy, not the enforcement.

**`password_hash` stays `NOT NULL`.** A nullable credential column is one every
comparison has to remember to check. An SSO-only account stores a sentinel that
is not a bcrypt hash at all, so bcrypt refuses it structurally whatever is
presented — and `Authenticate` refuses such an account *by name* before it
compares anything, so the rule is a statement in the code rather than a property
of a string constant. The decoy compare still runs, so the refusal costs the
same time as any other.

**State is a row, and it is bound to a browser.** A `state` is single-use,
expiring, and stored as a digest; the claim is a guarded `UPDATE` whose `WHERE`
clause carries all four conditions, so a forged, replayed, expired or
wrong-provider callback matches zero rows. Single use is why it is a table and
not a signed cookie: a self-contained token verifies just as well the tenth time
it is replayed. Beside it, a short-lived cookie this host sets on the browser
that started the flow — a state nobody can bind to a browser is one anybody can
hand to anybody, which is how a person is signed in as an account they do not
own without noticing. PKCE (S256) and a nonce where the provider is OIDC, and
the nonce comparison is unconditional: an empty expectation would otherwise
mean "do not check", and a check that switches itself off when its input is
missing is not one.

**Cancelling retires the state too.** Single use has to mean used *or
abandoned*, never used or lingering. The provider-error branch used to redirect
before the state was ever claimed, so pressing cancel left a live row for the
rest of its ten minutes and the branch answered for a provider the state was
not issued for. It now claims through the same guard a completion does — best
effort, since whoever is being redirected has already been told the provider
said no, and a state it could not claim simply costs them the return to where
the flow began.

**The state table is bounded where it is written, not by a ticker.** The
endpoint that issues one needs no credential, so anybody who can reach the
dashboard can cause an insert. Issuing purges the expired rows in the same
transaction, which caps what is held at one TTL's worth whatever the rate; the
background sweep runs on the TTL and is for a host nobody is signing in to. A
cap on live states was considered and refused — it turns a flood into a
lockout, refusing sign-ins to the people the host exists for while costing
whoever caused it nothing.

**The ID token's signature is verified even though it arrived over TLS from the
token endpoint.** The specification permits skipping it there and the reasoning
is sound; it is still not what this host does. The check costs one cached key
set, it is the difference between trusting the transport and trusting the
issuer, and a flow that has never verified a signature is one nobody can safely
move to a front-channel response later. The algorithm is decided here and the
header is only compared against it, which is what closes `alg: none` and
HMAC-with-the-public-key.

**GitHub is not OIDC, and is adapted rather than duplicated.** No ID token, no
nonce, no PKCE on OAuth apps — so the flow is the same and only the last step
differs, asking GitHub's API who the access token belongs to. The address comes
from `GET /user/emails` and only from the entry that is both primary *and*
verified: `GET /user`'s `email` is the public profile address, frequently null
and never asserted to be verified, and taking the first entry in the list would
accept one somebody added minutes ago.

**Entra needs a directory, and `common` is refused.** Its multi-tenant endpoints
publish a templated issuer — literally containing `{tenantid}` — which no
token's `iss` can equal, so accepting one would mean dropping the issuer check
and letting any directory mint an identity for this host. Entra also issues no
`email_verified` and often no `email`; the address falls back to
`preferred_username` when that parses as one. What stands in place of the
missing claim is the tenant: every accepted token was minted by one directory
for one of its own members, and the address was assigned by that directory's
administrator rather than asserted by its holder. That is a different guarantee
from Google's, not a weaker one, and it is written down rather than left to be
inferred from an absent check. Mapping roles from group claims is a follow-up
and is deliberately not started.

**Registration is off by default and an upgrade does not open it.** The zero
value of the policy accepts nothing, for the same reason the approval policy's
zero value asks about everything: a setting that loosens on upgrade is the wrong
direction to be wrong in. An optional email-domain allow-list bounds who may
ask. Every rule is applied in one function, so a password registration and a
provider registration cannot diverge — one door that checks the policy and one
that does not is how a host ends up refusing sign-ups on a form while accepting
them through Google.

**The password door proves nothing, so it always waits.** Each provider
establishes the address before this host sees it. A form establishes that
somebody can type. That difference is what `RegistrationPolicy.StatusFor`
encodes, and it is the whole of the rule: approval-off lets a *proved* address
in without an administrator, and a password registration lands pending
regardless.

The combination it removes is the one worth naming. With registration on,
approval off, and an allow-list of `corp.com` — three switches a settings form
presents as independent — any anonymous caller could otherwise create an
*active* account for `boss@corp.com` and walk in holding read, propose and
approve. The allow-list means "who may have an account" through a provider and
only "what may be typed" through a form. Refusing in the code that acts on the
values rather than by cross-checking fields in the form: a form-level check is
one more thing to keep in step with the code that reads them. The setting says
so at the control, so it is a rule an operator reads rather than discovers.

**A self-registered account reaches nothing until somebody grants it
something.** The wildcard was the obvious default and the wrong one: it made
approving a stranger decide two things at once while presenting itself as one
— whether this person may have an account, and what they may reach. An empty
grant denies everything, which is the reading a principal has always taken of
one, and the Users page shows "Nothing" until an administrator lists some.

**A provider's display name is dropped rather than refused.** Every rule
`ValidateDisplayName` enforces is met by real names — an emoji joined with
U+200D, an Arabic name carrying a bidirectional mark, a long one — and nobody
here typed it. Refusing the registration over it would make an account
impossible for that person while the browser said the provider did not finish
and the log showed a validation error about a field they never filled in. The
name is cosmetic and never an identity, so an unusable one is discarded and the
account renders as its address. A name somebody types is still refused with a
reason: they can see the field and fix it.

**"Administrator" in the last-administrator guard means one who can
administer.** The role, not disabled, *and* not pending. A pending account holds
no capability whatever its row says, so counting one there would let the last
real administrator demote, disable or delete themselves — leaving a host with
nobody holding `admin` and nobody able to approve the pending account the guard
counted, which is not recoverable from inside the dashboard.

**Approving a registration is a privilege grant.** It is the moment somebody
gains the ability to do anything here, so it appends to the hash-chained trail
inside the transaction that performs it, naming the administrator who decided —
beside importing a server and classifying a tool, not in the settings history.
The status change is guarded on `pending`, so two administrators approving the
same registration produce one grant and one refusal rather than two entries
claiming something that happened once. A rejection deletes the row and keeps the
entry: the address and the provider account are free again, and what happened is
still answerable.

**Redirect URIs derive from the dashboard's own public address**, the
`server.frontend_public_url` setting. Not from the
request: a URI assembled from a `Host` header works when an operator tests it
from the same machine and fails for everybody else, and the header is set by
whoever is talking to this process. With no public URL configured there are no
buttons and the Authentication page says why, rather than generating an address
that will not work. Client secrets go through the settings machinery like every
other credential — encrypted at rest, withheld on read-back, never logged.

mcpd is not an OAuth authorization server. It was, and the endpoints were
unreachable in the deployment they existed for: signing in through a tunnel
needs mcpd reachable from the public internet, which is the one thing a tunnel
avoids. OpenAI's documentation states the authorization server "is not
automatically tunneled".

## Groups, and keys this host issued

Two axes, and keeping them apart is the whole design. A **role** decides what a
caller may *do* — read, propose, approve, administer — and `roleCapabilities`
is the only thing that knows the difference. A **group** decides what a caller
may *reach*: which plugins, and nothing else.

**Capability-carrying groups are a non-goal, not an omission.** With two roles
and four capabilities, a second bundle-of-rights mechanism would mean "why can
this person approve" is answerable only by reading both, and neither would be
explainable on its own. If the capability set ever needs to be finer than two
roles, the answer is more roles, in the one map that already decides.

**Effective grants are a union, computed in one place.** What a subject reaches
is its own grants unioned with the grants of every group it belongs to, and
`groups.Effective` is the only function in the process that works it out — one
SQL statement covering both subject kinds and both sources, so there is nothing
to keep in step. It is the same arrangement `Principal.Can` has for
capabilities: one choke point, so a rule applied there is applied everywhere.
Computing it in a second place would be the bug, which is why `User.Principal`
takes the grants as an argument rather than reading them off the row — a method
on an account cannot answer a question about other rows, and requiring the
value is what stops a staler answer being assembled elsewhere.

It is resolved per request, never frozen when a session or a key was issued.
That is what makes taking somebody out of a group take effect on their next
call rather than the next restart, and it is the property `Can` already had for
a pending account.

**Default none, at every level.** A new group grants nothing. A subject in no
group reaches nothing. A new key reaches nothing. The wildcard absorbs, so a
grant is never rendered as smaller than it means, and an empty list denies
everything — the reading a principal has always taken of one. The direction
matters more than the convenience: this is the same reason self-registration
went from `["*"]` to `[]`.

An account's *direct* grant may now be empty, which used to be refused. That
refusal was right while a direct grant was the only kind — an account with none
could never reach anything — and is wrong now that a group is how an account
usually gets its reach.

**The account that claims the instance is the exception, and it is not one.**
`CreateFirst` still grants the wildcard. That looks like a breach of the rule
and is not: the claimant is this host's administrator, with nobody above them
to grant anything, and an administrator can grant themselves any plugin in two
clicks. The wildcard therefore changes no security property — it only spares
the very first person a console that shows them nothing until they have
granted themselves access to see it. Default none is a rule about principals
somebody else decides for: a new group, an account an administrator creates, a
key, a self-registration. The claiming account is not one of those, and this
paragraph exists so the point is not re-argued.

**Deleting a group narrows, and strands nobody.** Its memberships go with it,
every member keeps its own grant and every other group it is in, and nothing
gains anything. It is allowed while members remain rather than refused, because
a group that has to be emptied first is one an operator empties in a hurry with
no record of what it held; the entry records the grant and how many members
lost it, and the page confirms with the count.

### A key is a principal

A static token in `auth.static_tokens` already carries a principal, a role and
a plugin list — it is an API key declared in a file. So a key is not a parallel
system: it is that declaration moved into the database, and what moving it buys
is revocation, an optional expiry, a last-used timestamp, and grants that
follow a group rather than a file.

**Only `CapAdmin` may create one**, because a key acts on this host with a role
and a reach and issuing one hands out both.

**A key has an identity of its own**, `key:<id>`, so the trail names *which key*
acted rather than a shared service identity. That is the reason the feature is
worth building: with a standing rule able to authorise a write unasked, "which
agent did this" has to be answerable. The identifier and not the name, because
a name is a rendering its owner can change.

**The secret is shown once.** It exists in the reply to the request that created
it and nowhere else: what is stored is a SHA-256, no endpoint reads one back,
no log line carries one, and no error body does. The digest is unsalted and
that is correct here for the same reason it is for a session token — 256 bits
from a CSPRNG has no dictionary to precompute, and a salt would only prevent
the lookup by digest that verification depends on.

**Revocation takes effect on the next request.** Nothing about a key is cached
between calls. The row is deleted by nobody: a revoked key keeps its row,
because an audit entry naming an identifier that resolves to nothing does not
answer the question the entry exists for.

**Expired and revoked are different facts, and the difference is
operator-facing.** An administrator chasing a connector that stopped working
needs to know which; a caller probing credentials must learn only that its
credential was not accepted. So the store returns distinct errors, the host
logs which, the Keys page shows the status — and `apikeys.Verifier` flattens
every refusal to `ErrUnauthenticated` on the way out.

**The last-used stamp is written at most once a minute**, guarded in the `WHERE`
clause. A write on every request would put SQLite's single writer on the hot
path of every tool call to answer a question nobody asks to the second.

### Static tokens are untouched

They are how a live deployment and its connectors authenticate, so they keep
working, unchanged, and nothing migrates them. `apikeys.Verifier` tries the
static set first — matched in memory, reaching no table — and only a credential
no file entry matches costs a query. A file token has no row, so no group can
widen or narrow it and no dashboard action can touch it.

**The two id namespaces cannot meet.** A key's identifier is generated and
begins `key_`; a file token's is chosen by an operator, and config validation
refuses one with that prefix. Deciding at startup, where the operator reads the
reason, beats deciding at verification time by whichever verifier answered
first — both land in the same `TokenID` field and the same audit column, and an
entry naming a credential has to name exactly one.

### Approval is one decision again

SSO left this open: a freshly approved account got `Plugins: []` and an empty
console, so approving a stranger decided two things while presenting itself as
one. Groups close it from both ends. `auth.registration.default_group` names a
group every registration joins — empty by default, so the zero value still
grants nothing and an operator has to fill it in — and approving may assign
groups in the same transaction as the status change, so there is no window in
which an approved account exists reaching nothing.

The setting holds a *name* rather than an identifier, because an operator types
it into a field and a name is what they can type. A name matching no group
grants nothing rather than failing the registration: a group renamed or deleted
underneath a setting nobody has revisited must stop granting, not start
refusing sign-ups.

### What is audited

Every act here changes what somebody can reach, so every one of them appends to
the hash-chained trail inside the transaction that performed it, naming the
principal who acted — beside approving a registration and classifying a tool,
not in the settings history.

| entry | subject | detail |
|---|---|---|
| `group.created` | the group's name | id, grant |
| `group.updated` | the group's name | id, grant, **the grant it replaced** |
| `group.deleted` | the group's name | id, grant, how many members lost it |
| `group.member_added` | the group's name | id, member kind and id |
| `group.member_removed` | the group's name | id, member kind and id |
| `apikey.created` | the key's id | name, role, grant, groups, expiry |
| `apikey.rescoped` | the key's id | each field, **and what it was** — name, role, grant and expiry alike |
| `apikey.revoked` | the key's id | name |

Two rules run through the table. A privilege change records what it changed
*from* as well as to, because an entry carrying only the new value leaves "what
did this widen" unanswerable — which is why a re-scope carries the previous
name, role, grant *and* expiry, an extension from next month to next year
being a grant of a year's more reach. And an act that changes nothing writes
nothing: adding somebody who is already a member is not an error and is not an
entry, because a trail that records non-events is one nobody reads carefully.

**Every membership is one entry, whichever act produced it.** An administrator
adding somebody on the Groups page, an account created straight into a group, a
key issued into one, an approval assigning one, and a registration joining the
default all go through `groups.AddMemberAudited` and all write
`group.member_added`. The alternative — letting each act describe its own
memberships and nothing else — makes "how did this person come to reach that
plugin" answerable only by knowing in advance which act to look for, and leaves
a hole wherever a path forgot. The act that caused it still names its groups in
its own entry; that is context, not a second answer.

A default-group membership is recorded against `system:registration` rather
than against the registrant or an administrator. Neither of them decided it — a
setting did — and attributing it to a person would put a decision in the trail
they did not make, which is the same reason an auto-approved operation is
attributed to `system:policy`.

## Telling what it is doing

`/health/live` and `/health/ready` are on the MCP listener because a load
balancer in front of that port has to reach them without a credential, and they
carry aggregate state and nothing else for the same reason.

`/metrics` is not on that listener. It is on the **dashboard** one, and the
difference is deliberate: the MCP port is what a third party reaches through a
tunnel, and metrics name every mounted plugin, every tool, how long each named
upstream takes to answer, and how often each fails. That is exactly the
operational detail the readiness probe is careful not to carry. The dashboard
listener already has the right audience — operators, on an internal interface —
and the rest of the operational detail already lives there.

It takes `read`, which is what a read of this host's own state takes
everywhere else here, and a Prometheus satisfies it with a static token like
any other machine caller. `metrics.public` drops the check for a deployment
that has already fenced the port off to a monitoring network; it is off by
default and config validation says plainly what turning it on means. Switching
metrics off leaves the route answering 404 rather than the dashboard's own
shell, so a scrape config pointing at a host that is not serving them fails
instead of quietly parsing HTML.

**Some numbers are not this process's to keep.** How many operations are in
each state is answered by SQLite, and a counter incremented in Go would
disagree with it after every restart and every prune — and would never mention
the row that has been sitting in `indeterminate` since Tuesday. Those are read
when a scrape arrives, bounded by their own timeout so a busy database costs
the series rather than the response. Counters for things that only happen in
memory — a tool call, a refused proposal, a cache hit — are ordinary counters.

Every series is there because somebody asks the question it answers; the list
and the question each one is for are in `internal/observability/metrics.go`.
Two cardinality rules hold throughout: a label is a class and never an
identifier — a metric labelled with a device address is a new time series per
device — and a plugin is handed an interface narrow enough to report its own
cache and its own upstream latency and nothing else, so an integration cannot
invent series this host then has to carry.

## Where configuration lives

One authority per setting, and it is the database for all but four.

`config.yaml` holds `storage.path`, `secret_key_ref`, `server.listen` and
`server.frontend_listen`. Nothing else. Two of those cannot move for a reason
that is not a judgement: the database cannot say where it is from inside
itself, and the key every stored credential is encrypted under cannot be
encrypted by the system it unlocks. The other two are a judgement, and it is
worth stating rather than assuming — a bad bind address in the database locks
an operator out with no interface left to fix it on, so the file is the
recovery path, and it is only a recovery path if it is the authority.

Everything else is a `settings.Field`: the address to advertise, the
dashboard's own address, TLS mode, whether the dashboard runs at all, the five
listener timeouts, how long a statement waits for a lock, relaxed durability,
the session TTL, the whole approval policy, logging, and the tunnel.

The tunnel's own credentials are the exception, and they are an exception to
the shape rather than to the rule. `settings` is a flat key/value store, so
holding several ChatGPT accounts there would mean synthesising keys like
`tunnel.account.3.api_key` — a table with the constraints left out. They live
in `chatgpt_accounts` instead, where a name can be unique and a credential can
be NOT NULL. What stays in `settings` is the assignment: which account a tunnel
connects with, beside the tunnel id it already held, because those are one
decision made on one page.

**The argument is the record, not the tidiness.** Editing `config.yaml` leaves
nothing behind: no actor, no before, no after, and nothing the dashboard can
show. Every write through `settings.Store.Apply` lands a row in
`settings_history` naming who made it and what it replaced. Moving a value is
moving it from a place where changes are invisible to one where they are not.

**The precedence rule, stated once.** For a key that moved, the database is the
only authority. The file is not consulted to run, and neither is the `MCPD_`
override that used to apply to it. What the file and the environment get is one
turn: on the first start after an upgrade — and on the first start of a new
deployment, which is the same code path — whatever they supply is imported into
the store, once, attributed to `system:config-import`, and recorded like any
other change. `config.KeyConfigImported` holds the record of what was imported,
what was kept, and what was refused.

The import never overwrites. A key the store already holds keeps its value: one
was chosen and the other was inherited. A value the settings schema refuses —
`tunnel.role: approver`, from before the roles collapsed to two — is left out
and named, so the file cannot be a way around validation the dashboard applies.
The tunnel's `api_key_ref` is resolved once and stored encrypted, which puts
the last credential-shaped thing the file referenced where every other one
already is. It has since moved again, from `settings` onto a ChatGPT account,
by the same one-turn rule — see Tunnels.

Afterwards the file is ignored, and any key still in it whose value *disagrees*
with what the host is running is named at startup. Disagreeing rather than
merely present: a container that keeps setting `MCPD_PUBLIC_URL` to the address
already stored has no problem and should not be told it has one. What must
never happen is two sources of truth quietly differing, which is how an
operator edits a file for an hour and changes nothing.

**Refusals became warnings, deliberately.** A bad value in a file is fixable
with an editor, so validation could be fatal. A bad value in the database is
fixed on a page that a refusal to start would remove, so `Effective.Warnings`
says what is wrong and mcpd comes up. Write-time validation is unchanged and is
the same `settings.Validate` the dashboard uses.

**Two circularities, resolved rather than avoided.** How the SQLite pools are
opened is stored inside the pools: `openStorage` opens once with the defaults,
reads the two values, and reopens only if they differ. How much the logger says
and in what shape is stored in a database the logger has to exist before
opening: `observability.NewSwitchableLogger` builds both handlers up front and
picks per record, so applying the stored values needs no restart and no second
open. Before the store holds anything, the file's values seed both — the same
one turn.

**What needs a restart says so.** The address, the dashboard's address, the
drain budget, the session TTL, the approval policy and logging are read at the
point of use, so a change reaches the next request. TLS mode, whether the
dashboard runs, the four listener timeouts and both storage settings configure
things built once at startup; they are declared `ApplyRestart`, the dashboard
puts a *needs a restart* chip on them, and the save reports them under
`restart_required`. A field snapshotted at startup and a field claiming to
apply live are the same claim made twice, and the dashboard is only honest
while they agree.

**Where the secrets are, plainly.** There are no plaintext passwords on disk.
Account passwords are bcrypt in `users`. Plugin credentials, SSO client secrets
are encrypted at rest in `settings`, and the ChatGPT keys in
`chatgpt_accounts` under the same cipher. API keys are digests —
mcpd checks one, it cannot read one back. Static tokens in the file are
references resolved at startup, never values. The single plaintext secret is
`MCPD_SECRET_KEY` in `data/.env` at mode 600, and it cannot be anything else:
it is the key everything above is encrypted under, and a lock does not hold its
own key.

## Storage

Tables: `operations`, `operation_transitions`, `execution_attempts`,
`idempotency_records`, `outbox_events`, `audit_events`, `audit_prune_gate`,
`plugin_state`, `plugin_overrides`, `settings`, `settings_history`, `users`,
`user_sessions`, `user_identities`, `sso_states`, `groups`, `group_members`,
`api_keys`, `mcp_servers`, `mcp_server_tools`, `ca_certificates`,
`chatgpt_accounts`.

Migrations are forward-only and checksummed; a changed file that has already
run is an error rather than a silent divergence. There is no down path —
rolling a schema change back on a database holding approved operations is a
data-loss decision, not an automated one.

`audit_events` is hash-chained and append-only, enforced by triggers. Pruning
opens `audit_prune_gate` for the duration of one transaction; without the gate
open the table refuses deletion, including from a `sqlite3` prompt.

Two idempotency mechanisms, and they must agree about time.
`idempotency_records` carries a TTL and implements the semantic: an identical
replay inside the window returns the original operation, and the same key with
a different body is refused. `ux_operations_idem` is a *partial* unique index
scoped to live states — one live operation per intent, while a settled one may
be proposed again. It was once permanent and blind to state, which made an
expired proposal un-retryable forever.

## Plugins

In-process plugins register in `registerPlugins`; the switch is the complete
list a binary can serve. Out-of-process plugins are ordinary programs speaking
the `sdk` protocol over stdio, mounted from the plugins directory. A third
kind is a remote MCP server, described below.

**A subprocess's stdio is multiplexed, and a caller's deadline is its own.**
One goroutine owns the pipe's read side for the process's whole life and hands
each frame to the caller whose id it carries; the write lock covers the write
and nothing else. What that buys is not throughput so much as independence.
Holding one lock across the whole round trip made every caller queue behind the
one in flight — and worse, each of them had already started its own timeout
before joining the queue, so the first to acquire the lock immediately found its
deadline gone and killed the plugin to recover a pipe that was not blocked.
A slow call took down every other caller, including the ones about to succeed.

Now a timeout fails one call. The process is left alone, a late answer is
discarded by id rather than handed to the next caller, and a plugin that has
genuinely stopped answering fails every call and reports itself unhealthy. Two
things still end it: a frame over the cap, because the rest of the line has not
been read and the stream position is no longer known, and shutdown.

The write lock is a channel rather than a mutex, because it has to be possible
to give up on. A plugin that stops reading its stdin will eventually block a
writer, and a `sync.Mutex` would queue every other caller behind that with no
way out — which is the shape being removed. Acquiring selects on the caller's
context, so a caller that cannot get the pipe fails on its own deadline.

A mutation declares its target, its desired state, whether observing the result
confirms it, and how to observe it. The host plans against live upstream state,
freezes the payload, executes at most once, then re-observes and compares — the
last step only when the mutation said it would mean something. Verifiability is
declared rather than inferred from an empty desired state, because that field
is ambiguous: for a delete it means "the target should be gone", which is a
real thing to observe, and for a write that cannot be read back it means
nothing at all.

The plan travels from `Plan` to `Apply` in the argument. Nothing may key it on
the parameters instead: two live proposals of the same change share those, and
whichever executes first would consume the other's plan.

The rule that matters most: if `Apply` cannot establish whether its write
landed, it must return `sdk.Indeterminate`. Anything else tells the host the
write did not happen and permits a retry that applies the change twice.

## Remote MCP servers

A remote MCP server is somebody else's, reached over the network. It is mounted
as an ordinary plugin — same endpoint shape, same per-plugin scoping, same
authorization gate, same audit — and the difference is trust, carried by
`Descriptor.Runtime`. Everything mcpd was built with is `builtin`; only this is
`mcp`.

Three of the host's rules change for that runtime, and each is applied where
the rule lives rather than by the code that builds one:

- **No mutations.** The registry refuses one. There is no propose/approve story
  for a third party's tool: the host cannot plan against its state, cannot
  freeze a payload it does not understand, and cannot re-observe the result.
- **Tool names follow the specification, not the house style.** `getWeather`,
  `search.docs` and `read-file` are all valid upstream, and the house rule
  rejects every one. Names are passed through unchanged — a normalised name is
  one the far end does not answer to — bounded only by the 128-character limit
  on the prefixed name.
- **Attachment is per tool.** One malformed descriptor out of three hundred
  costs that tool. The far end's catalogue is not something an operator can fix.

**Register reads SQLite, never the network.** This is the load-bearing decision.
The tools a remote server offers are snapshotted at discovery and mounted from
that snapshot, so a host restarting while the far end is down comes up serving
exactly what it served before and reports itself unhealthy. Calling `tools/list`
at boot would give a host with no tools and a model that reasonably concludes
the integration was removed.

**Every administrative act is in the audit trail.** Importing a server, running
discovery, classifying a tool, turning a server on or off, and removing one
each append to the hash-chained `audit_events`, inside the transaction that
performed them, naming the principal who acted. Enabling a tool is a privilege
grant — it hands every caller of that plugin a path into a third party's code —
so it belongs where privilege grants are recorded rather than in the settings
history. A toggle that changes nothing writes nothing: a trail that records
non-events is one nobody reads carefully. Reads are deliberately not audited
per call; this is about state changes.

**Nothing the server says is authority.** `tools/list` is a claim. A tool
arrives `pending` and is not served until an administrator classifies it, and
what they classify is a *descriptor* rather than a name — `descriptor_hash` is
in the `WHERE` clause of every state change, so a tool whose schema changed
underneath an approval cannot inherit it. Its annotations are hints the MCP
specification itself says not to trust, so nothing branches on them. An input
schema that is not an object disqualifies the tool: substituting a permissive
one, which the out-of-process adapter does for a binary the operator dropped in
themselves, would throw away the only argument validation there is.

Lifecycle: import a `server.json` → discover → classify each tool → it mounts.
The document is stored verbatim and validated against a **vendored** copy of the
schema; nothing is fetched at runtime, and a document declaring a `$schema` this
build does not read is refused rather than parsed optimistically. Its inputs
become settings fields, so a credential is typed into the dashboard, encrypted
at rest, and resolved store-then-file-then-default like every other — never read
out of the document.

**A credential goes to the address it was configured for, and nowhere else.**
The configured headers are pinned to the configured origin — scheme, host and
port — in two places independently: the client refuses a redirect off it, and
the transport that injects them checks again before it does. Go's own defence
does not reach this. The standard library strips `Authorization` and `Cookie` on
a cross-domain redirect, but only for headers set on the original request; it
cannot see one a RoundTripper adds per hop, which is what this one does. Without
the pin, a server answering `302 Location: https://attacker.example/` — after a
compromise, a DNS change, or an expired domain someone else registered — is
handed the operator's API key. The same check refuses a hop that reaches further
into the deployment's own network than the configured endpoint already did, so a
public server cannot steer this host at a metadata service. It is judged against
the endpoint rather than absolutely, because a server on loopback or on the
LAN is an ordinary thing to configure and is not made safer by being refused.

Rate limiting is per server as well as per tool, because thirty tools behind one
address are one upstream.

## The catalogue

**Enumerated once a day, not proxied per request.** Each catalogue is walked to
the end and held locally; browsing answers from that. The arrangement it
replaced asked four third parties per request and merged the windows that came
back, which was visibly wrong in three ways.

A page was as long as the catalogues happened to make it. Each source was asked
for a window, entries this host cannot import were dropped, and what survived
was however many survived -- so Smithery contributed ten rows and Docker
contributed two, from identical code. Nobody reading that could tell it from
broken paging.

The size of the catalogue could only be estimated, because a source that pages
an opaque cursor cannot say how many servers it holds. And search meant four
different things at once: one catalogue matches names, another runs a relevance
engine that returns its whole catalogue ranked, and a page merged from both was
mostly the second's idea of "related" -- so searching for a product returned
rows that did not mention it anywhere.

Holding them settles all three. Pages are a consistent length because this host
decides the length, the count is a count, and searching is one rule applied to
one set: every term must appear in the name, the suggested name, the title or
the description. Deliberately not the URL -- a host name matching a term
nothing visible mentions is a hit nobody can account for by looking at the row.

**What it cannot do is make a catalogue hand over more than it will.** Smithery
publishes ten thousand servers and lists five hundred; the official registry
pages twenty-five thousand and publishes no total at all. So the figure
reported is how many servers this host can actually offer, which is exact and
deduplicated, rather than what exists in the world, which no caller here can
know. The same server in two catalogues is one server, identified by the
endpoint it dials, because the catalogues do not agree on names.

The first read of the day walks the catalogues and takes about fifteen seconds;
every read after it is served from memory. Nothing is fetched until somebody
opens the Marketplace, so a host nobody browses pays for none of it.



Hand-authoring a `server.json` to add a server somebody else already published
is copying. `internal/registry` browses the public catalogues of MCP servers so
an operator can pick one instead.

Four of them today.

| catalogue | what it is | default |
|---|---|---|
| `registry.modelcontextprotocol.io` | the official registry, where a publisher registers a server themselves | on |
| `api.pulsemcp.com` | PulseMCP's v0.1 sub-registry, ~22,000 servers, mostly mirrored from the official one | **off** |
| `docker/mcp-registry` | Docker's curated catalogue | on |
| `registry.smithery.ai` | Smithery's registry, ~10,500 servers, all hosted behind one gateway | on |

Docker's is built from
[docker/mcp-registry](https://github.com/docker/mcp-registry), which is MIT
licensed — the notice travels with the vendored fixtures and with every
document composed from an entry, in its `_meta`. Any of the four can be
switched off under `catalog:` in the configuration file; a deployment with none
gets an endpoint that says so.

Reading a catalogue is an HTTPS fetch of somebody's metadata. Nothing about
Docker's involves Docker: no daemon, no container, no image.

**A source is on by default when it is useful without a credential.** That is
the whole of the rule, and it separates the two new ones. Smithery's *servers*
need a key to dial, but its registry does not need one to browse — so an
operator with no Smithery account still gets ten thousand descriptions, a
search over them, and a row that says which ones would ask for a key.
PulseMCP's v0.1 API authenticates every request, so the same deployment would
get a page of 401s; a source that can only report its own misconfiguration is
worse to default to than an absence, and it is off until a key and tenant are
configured. Config validation refuses to start with it on and either missing,
because "mounted and failing every page" is the state nobody wants.

That is also why the obvious PulseMCP integration is not the one built. The
unauthenticated `v0beta` API — `count_per_page`, `offset`, a `remotes[]`
carrying `url_direct` and `cost` — is being switched off on a published
schedule that reached 50% of requests randomly failing in June 2026 and reaches
100% in September. Measured on 2026-08-23, three of six requests came back
`410 API_SUNSET`. Building on it would have shipped a source already half dead.

**Two catalogues speak one API, so there is one reader.** PulseMCP's v0.1
implements the Generic MCP Registry API — the same `{server, _meta}` rows,
`metadata.nextCursor` and pass-through `server.json` the official registry
serves. What differs between the two is a base URL, two credential headers and
the `_meta` key each writes its lifecycle facts under, which is a parameter
list rather than a second implementation. `generic.go` is the reader;
`official.go` and `pulsemcp.go` are its two configurations.

**Smithery's listing is bounded, and says so.** Its API reports ten and a half
thousand servers and then refuses to page past five hundred of them, whatever
page size is asked for — at `pageSize=100` `totalPages` is 5, at `pageSize=3`
it is 167, and both are the same five hundred rows. Search is not bounded that
way, so `?q=` is passed upstream rather than used to filter a page that was
already truncated: filtering locally would present the top five hundred as the
catalogue, and a search for a server at position nine thousand would come back
empty with nothing saying why. A browse carries a note on its `SourceStatus`
saying the listing stops and search reaches the rest, because a page whose last
row is the five hundredth looks exactly like the end of a catalogue.

Smithery's paging also repeats itself: those five hundred rows held two hundred
and sixty-nine distinct servers when measured, and pages one and two shared
thirty-nine. Page one is stable when refetched, so it is not jitter a retry
would fix — the ordering is by popularity and is not a total order. The window
is fetched whole (page one, then the rest concurrently) and deduplicated by
name before anything is paged out of it.

**Then it is ordered by use, which is the only real quality signal any of the
four publishes.** Every Smithery listing row carries a `useCount` — how many
times Smithery has been asked to call that server — and the numbers are not
close: the head of the catalogue is in the tens of thousands and the tail is at
zero. A default view of ten servers out of twelve thousand is a sample either
way, and "ten servers people use" is a far better sample than "ten servers".
`verified` breaks a tie, because Smithery vouching for a listing is worth
something between two servers nobody has called and nothing at all against one
with fifty thousand calls behind it; the name breaks the remaining ties, which
is what makes the ordering *total* — and a total order is what lets the cursor
be a resume point rather than an offset into a list that reshuffles. The cursor
is the rank key rather than the last name, for exactly that reason.

**There is no cross-source ranking, because there is no cross-source signal.**
The obvious next step is a merged "most used" order, and it cannot be built
honestly. The official registry and Docker publish no usage figure at all.
PulseMCP publishes one, but it is `visitorsEstimateMostRecentWeek` — unique
visitors to a listing page — which is not the same measurement as a count of
tool calls and does not become one by being divided by something. Two of four
sources silent and the other two counting different things is not a ranking
waiting for a normalisation; it is a normalisation this host would have to
invent and then present as a fact. So each source is ordered by the best signal
it actually has, `Multi` interleaves them round-robin, and the page says it is
a sample rather than a top ten.

**"Most used" is therefore a narrowing rather than a ranking.** The count is
real and worth having, so `Entry.Uses` carries it — absent, not zero, where the
catalogue publishes none, because "nobody told us" and "nobody has called it"
sort together the moment they share a value. `sort=most-used` covers the
catalogues that publish the figure and leaves the rest out, naming them in
`Sources` with the reason; a source declares the capability through
`UsesReporter` rather than having it inferred, since the decision has to be
made before any row is in hand. With one such source configured that is a
genuinely total order over what is shown, which is the only order here that is
global. The number itself is on the row — "87,579 calls" — because a figure an
operator can check is worth more than a badge that asks to be believed, and
because it explains the ordering without a legend.

**The other two orders reach as far as the page and say so.** `name` and
`recently-updated` sort what `Multi` assembled, after it is assembled. A true
global ordering would mean holding twenty-four thousand entries from behind
four opaque cursors that no source will sort for us, so it is not offered and
not claimed: the dashboard sorts everything loaded so far and the line under
the control says that is what it is. Ordering cannot disturb paging, because
every source's position is recorded as rows are taken and rearranging taken
rows changes nothing about where each source resumes.

**A cursor belongs to one question.** It says where each catalogue resumes and,
by omission, which of them are finished — both true only of the question that
produced it. Carrying a most-used cursor into an unscoped listing would read
three catalogues as exhausted and drop them from every page after it. So the
cursor carries a fingerprint of the search, the order, the scope and
`include_unaddable`, and a mismatch restarts the listing.

**Scoping to one catalogue is the grouping that is actually there.** The four
differ in kind — Smithery hosts and keys what it lists, the official registry
is where a publisher registers their own, Docker's is curated — and `source=`
scopes to one. A name this host does not have is refused rather than ignored,
for the same reason a misspelled selector in an approval rule is: a filter
silently dropped answers a question nobody asked.

**Categories are not, and are deliberately not built.** Docker's `metadata.
category` is real and complete within Docker, and it is the only taxonomy any
of the four publishes. It was measured before being designed for: Docker's
catalogue is around three hundred entries of which roughly twenty-nine are
remote servers this host can add, and those spread across a dozen or more
categories. A filter that cuts twenty-nine rows — one or two pages, scoped by
the source filter above, scannable whole — into buckets of two is a control
that looks broken rather than one that helps. Sorting and filtering earn their
place against a list too long to read, and that list is Smithery's ten
thousand, which publishes no categories at all.

**One Smithery key opens every Smithery server, and it still lives in the
per-server settings.** Every hosted server is at
`server.smithery.ai/{qualifiedName}/mcp`, streamable-http, `401 invalid_token`
without an `Authorization` header — so the composed document declares that
header with a `{SMITHERY_API_KEY}` placeholder behind it, marked secret, which
is the shape Docker's entries already produce. The key then arrives the way
every other credential does: typed into the dashboard, encrypted at rest,
resolved store-then-file-then-default, never written into the stored document.

Holding it once for the source instead was considered and refused. It would
have to be either substituted into the document to be used — a credential at
rest inside a stored, hashed document, which is the one thing the import path's
verbatim storage makes indefensible — or resolved at dial time from a store the
plugin does not belong to, which is a second credential path beside the one
every other plugin uses and a hole in per-plugin scoping. The cost of refusing
is real and is worth naming: an operator importing four Smithery servers pastes
the same key four times, into four fields that each say what they are for.

**It finds documents; it does not install them.** Selecting an entry hands its
`server.json` to the same import endpoint a paste goes through, and everything
downstream is unchanged: the same validation, the same derived settings, the
same discovery, the same per-tool approval. There is no second import path,
which is the only way to be sure the catalogue cannot become a way around one
of those steps.

That is also what decides whether an entry is offered at all. Addability is not
"does it have a `remotes` array" — it is the two calls the import endpoint
makes, both of them: `mcpservers.Parse` on the document, then `mcpremote.Fields`
on the result. The second is not redundant. `Parse` judges the document;
`Fields` derives the form an operator would fill in, and refuses things `Parse`
accepts — an input declaring choices whose default is not one of them, or a
field the settings catalogue will not take. Checking only the first is how this
offers an Add button that fails, which is the one thing it exists to prevent.

This is the reason `internal/registry` imports `internal/plugins/mcpremote`,
and so is not the leaf it otherwise would be. The alternative was to
re-implement the acceptance rule beside the catalogue, which is the same bug
with an extra copy of the code to keep in step.

**Remote servers only, and a listing does not show the rest.** Roughly half of
the official registry, three quarters of Docker's catalogue, and the servers
Smithery does not host are published solely as something to run locally — an
npm package, a container, a command. This host does not run those. Docker's
`type: server` and `type: poci` entries are exactly that case, and so is an
entry reachable only through an OAuth flow Docker's own gateway performs: this
host sends a credential an operator configured, and the entry does not say
which header would carry the one that flow obtains.

They used to be listed, greyed, with the reason, on the argument that "why is
the thing I came for not here" is a worse question than a row that answers it.
That was right at thirty rows a page and wrong at ten. An operator who used it
reported the noise as worse than the missing answer, and the arithmetic agrees:
a page of ten that spends five rows explaining refusals is a page of five. So
`Multi` drops them, server-side, *before* the paging — which is the only place
it can be done and still have ten rows mean ten usable rows.

Nothing about the machinery is weaker for it. Addability is still decided by
`mcpservers.Parse` and `mcpremote.Fields`, both of them; `addable` and its
reason are still on every entry; `GET /api/catalog/{name}` still explains a
refusal in full, because somebody who came looking for one server in particular
is owed the answer; and `?include_unaddable=1` still returns them for an
operator who wants to see what is being withheld. What changed is what a
*listing* is for.

**A limit bounds the page, not each catalogue.** It did not, and the bug was
worth more than it looks. Every source was handed the caller's limit and
honoured it independently, so a request for ten returned thirty and a request
for thirty returned ninety — three sources' worth, merged. The API said one
thing and the endpoint did another, the dashboard rendered and shipped three
times what it asked for, and an operator reading ninety rows concluded the
catalogues held ninety servers between them. They hold something over twelve
thousand.

So `Multi` pages. Each source is asked for a window of twice the page, with a
floor of twenty; the windows are merged in preference order, filtered,
deduplicated, and handed out `limit` at a time. Sources are *read* round-robin
even though duplicates are *resolved* in preference order, because reading in
preference order would mean the second catalogue was never reached until the
first's twenty-four thousand entries ran out.

That forces the cursor to carry more than a cursor. A bounded page very often
stops halfway through a source's window, and a source's own cursor can only say
"the next window" — resuming there would silently drop the other half of every
window in every catalogue, which nothing but a total would reveal. So each
source's position is a pair: its own cursor, and how far into that window the
last page reached. Re-asking for a half-read window is free, because the
per-source cache is in front of it — which is also why the over-fetch pays for
itself. Measured against three catalogues at a 120 ms round trip each, page one
costs the same as it always did (both shapes fan out concurrently and both wait
on the slowest catalogue) and page two went from a 251 ms fan-out to a cache
read, while the default listing's payload fell from 47 KiB to 5.9 KiB.

**How big is it.** A page of ten out of twelve thousand looks exactly like a
catalogue of ten, so the page says roughly how many servers can be added and
the search box sits next to that number rather than above a grid. It is an
estimate and is rendered as one — rounded down to two significant figures and
carrying a `+` — because it cannot honestly be anything else. Only two of the
four sources report how much they hold: Smithery sends a `totalCount`, and
Docker's catalogue arrives as one document whose length is the count. Neither
reports how many of its servers *this host* would accept, and finding out for
certain means parsing twenty-four thousand `server.json` files behind a page
load. So the ratio is measured over the documents that were parsed anyway while
the page was built, and applied to the size the source gave. Smithery's sample
is its most popular five hundred and so runs optimistic; the two sources that
report no size contribute only what was seen and so run far short; a source
that did not answer contributes nothing and the page says so, because a total
that does not move when a catalogue goes down is worse than a smaller one.

**The catalogue shows no remote imagery, and a monogram is not a fallback.**
Smithery, Docker and `server.json` each offer an icon URL. This host read them,
validated them and put them in an `<img src>`, and not one ever rendered: the
dashboard sends `img-src 'self' data:`, so every remote image has always been
blocked and every entry has always drawn its monogram. The bug report that
found this named a Google favicon-service URL a publisher had put in
`icons[].src`, which returns a redirect to `text/html` and would not have
rendered even with the header open.

So the fetch is gone rather than the header. Loosening `img-src` would let a
catalogue entry make an operator's browser call an address a third party chose,
which tells that party which servers are being looked at from inside this
deployment — a decoration is not worth that, and it is the same reason nothing
here fetches such an address server-side either. What remains is
`web/src/pages/marketplace/monogram.ts`: two letters on a colour derived from
the name, contrast-checked against both themes, and it covers every entry
because it needs nothing from anybody.

Worth recording because the first verification was wrong in an instructive way.
The icon URLs were tested with `curl`, which does not enforce CSP, and 24 of 25
"loaded" — which proved the addresses resolve, not that the page could display
them.

**A composed document is still a document.** Docker's format is not
`server.json`, so an entry is translated into one — the derived name says where
it came from, `${ENV}` in a header becomes a `{placeholder}` with a variable
behind it marked secret, and the result goes to the same import endpoint, is
judged by the same two calls, and is stored verbatim as composed. The
translation is byte-stable, because the import path hashes what it stores.

**Five `server.json` formats are read, not one.** Every dated schema published
to date is vendored beside the current one, and an earlier document is
translated into the internal model explicitly rather than parsed optimistically.
What actually moved between them is small and is listed in `schema.go`:
2025-07-09 spelled an input's flags `is_required` and `is_secret`, and
`remotes[].variables` — the map that says what a `{placeholder}` in a url means
— arrived only with 2025-12-11. The first is read under both spellings and
OR-ed, because that direction can only add protection to a credential and the
other can only remove it. The second is not read where the format does not
define it: an earlier document carrying a url placeholder is refused with its
version named, because substituting from a map the format never had would be
this host inventing a meaning and then dialling the address it produced. A
`$schema` that is none of the five is still refused — the pin is by URI, so the
right date at the wrong address is not that format.

**Nothing about the catalogue is on a startup path.** Every client is
constructed at boot and reaches nothing; the first fetch happens on the first
request that asks for one. A catalogue that cannot be reached serves what it
last saw, marked stale with the time it was fetched. A third party being down
is not this deployment's failure and is not worth a page that will not render —
but neither is it worth pretending the data is current.

**How long an answer is reused is the catalogue's to say.** A single hardcoded
TTL is wrong in both directions at once, and measurably so: the official
registry sends no `Cache-Control` and no validator at all, Docker's CDN sends
an `ETag` and a `Last-Modified` and no policy, and other catalogues send
`no-cache` or grant four hours to a shared cache and a day of
`stale-while-revalidate`. So `Cache-Control` is honoured where it is sent —
`s-maxage` in preference to `max-age`, because mcpd is a shared cache and not
one person's browser — `Age` is deducted, and the configured default stands in
only where a catalogue said nothing. `no-cache` with no validator to revalidate
against becomes a very short life rather than being ignored. A stale answer
inside the window a catalogue granted is served immediately and refreshed
behind it, one refresh per key, owned by the cache and cancelled at shutdown.
A refresh sends `If-None-Match` and `If-Modified-Since` when a validator is
held, which turns re-reading Docker's 567 KiB catalogue into a `304`. One
`server.json` is held longer than a listing, because it is a different question
keyed by a stable name; "no such server" is held for seconds, because a name
that 404s today is a server published tomorrow. `?refresh=1` bypasses all of it
for one request, for the administrator standing in front of a catalogue that is
visibly behind.

**One source's failure is one source's.** Sources are asked concurrently and
the whole fan-out is bounded, so the slowest catalogue does not decide how long
a page takes. What arrived is served, and the response says which catalogues
answered, which were stale, how many entries each contributed, and what went
wrong with the rest — a shorter list that does not name the missing catalogue
reads as "there is nothing else" rather than as "we could not ask". A page is
an error only when nothing answered at all.

**The memory bound is on the process, not on each catalogue.** One store behind
every cache, because a cap each source gets its own copy of is a cap a fourth
source silently quadruples.

That store is `internal/cachestore` and it is not a cache. It is a bounded map
of timed entries plus the rule that six callers asking one question at the same
moment should cost one answer, and it holds no policy at all — because the
policy differs between the two things that use it, and a store that decided it
would be one of them wearing a general name. Everything a catalogue is
particular about stays here: `stale-while-revalidate`, validators, and the
short memory of a name that 404s.

**The registry's content is a third party's text, arriving in whatever quantity
they choose to send.** The response is bounded before it is decoded, the entry
count per page is capped, and every field is bounded and stripped of control
and invisible-formatting characters before it is stored or returned.

**Deduplicate by name within a catalogue, by address across them.** The
official registry holds every version of every server and returns them all
unless asked otherwise; the query asks for `version=latest` and the
deduplication runs anyway, because "the far end promises one row per name" is
exactly the kind of promise whose failure shows up as a page listing the same
server four times.

Across catalogues a name cannot do the job — the official registry calls it
`app.linear/linear` and Docker calls it `linear`, and no rule turns one into the
other. The address does: thirty-two of the entries those two share resolve to
the same URL, and two entries that dial one endpoint are one server however they
are named. An entry with no address falls back to its own catalogue's name,
since nothing can establish that two unreachable entries are the same thing.

Which copy survives is preference order, and with four sources the order needs
a reason rather than a list. It is one idea applied four times — how far the
entry is from the party that operates the server:

1. **the official registry**, where the publisher registered it themselves;
2. **PulseMCP**, an aggregator, but a pass-through one that hands back that
   same first-party document unchanged;
3. **Docker**, whose entry is not a `server.json` at all but a document this
   host composed from a third party's description;
4. **Smithery**, which describes its own proxy in front of the server rather
   than the server.

The pair that actually collides is the first two, because PulseMCP mirrors the
official registry — which is exactly what the order is for. Smithery rarely
competes, and understanding why matters more than the fact: every Smithery
entry is addressed at `server.smithery.ai`, so a Smithery row and an official
row for what is recognisably the same project have different addresses and do
not merge. That is right rather than a miss. Dialling the publisher's endpoint
with the publisher's key and dialling Smithery's gateway with a Smithery key
are two different servers by every test that matters here — different address,
different credential, different party to trust — and merging them on the
strength of a similar name would hide one of two real choices.

Browsing takes `admin`. Everything it returns is public; the privilege is
making this host reach a third party from inside the deployment. Nothing about
it changes state, so nothing about it is audited — importing what it found is,
like any other import.

Every client is behind one interface, and the cache and the multiplexer are
themselves clients over clients: a cache in front of each source, so that one
being down is that source's staleness rather than the page's, and the
multiplexer in front of the caches, so that the handler still talks to a single
catalogue.

## Tunnels

One tunnel carries one address, so it is one connector in ChatGPT. A per-plugin
tunnel scopes by the principal it carries, not by URL — every tunnel binds the
in-process MCP server, so there is no URL to scope.

A tunnel's identity must not be written into a shared server. `AggregateServer`
caches by plugin set, which is right when identity arrives per request and
wrong for a caller carrying one, so a tunnel builds its own server. Getting
this wrong stacked middleware on every reconnect and let the first principal
answer for everyone.

**A tunnel connects with an account, and an account bounds it.** The
credential, the identity and the grant live in `chatgpt_accounts`; the tunnel
holds an endpoint and an assignment. Several exist because several ChatGPT
workspaces can share one host, and when they do the questions worth asking are
per workspace: whose key is this connector using, what may that workspace
reach, and which of them made the call somebody is now reading about. One
shared `svc:chatgpt` could answer none of them.

The two grants meet in `bindAccount`, and the narrower wins. A per-plugin
tunnel is already bound to its plugin, so assigning it to an account can only
ever reduce what it reaches, never widen it — which is what makes an account a
bound rather than a suggestion. An account that is not granted a tunnel's
plugin does not start that tunnel, and says which of the two refused.

**A tunnel with no account does not start.** Falling back to some other
account's key would have a connector quietly authenticate as the wrong
workspace, which is worse than one that does not come up. The empty assignment
resolves to the only account when there is exactly one — a deployment that has
never thought about accounts should not have to — and to nothing when there are
several, because choosing would be choosing whose credential a connector uses.

**The rate limit is a guard, not a quota.** The traffic runs inward: ChatGPT
calls mcpd, so nothing here is owed to OpenAI. `rate_per_sec` bounds what one
account can ask of this host and the systems behind it, so one workspace's
retry loop is not every other workspace's outage. Zero is unlimited and is the
default. One limiter per account, shared by every tunnel it owns — otherwise a
workspace given three connectors would get three times the allowance by using
all of them — and only `tools/call` is limited, because refusing a handshake
reads as a broken tunnel rather than a busy one.

The single set of credentials that predated accounts is carried into one on the
first start after the upgrade, keeping the principal it already had so the
audit trail stays continuous. That is one turn, guarded on the table being
empty, and it is the same rule the config import follows.

## Development

```bash
make check   # fmt, vet, test, dependency pinning
make race    # tests under the race detector
make web     # rebuild the dashboard bundle
make build   # dashboard, then binary
make docker  # build the image
```

The dashboard is a Vite/React app in `web/`, built into `internal/admin/dist`
so `go:embed` can reach it. It is a build artifact — changing the UI means
rebuilding the bundle before the binary serves it.

### The dashboard's own decisions

Four of them are worth writing down, because each was argued once and would
otherwise be argued again in a review.

**One table decides what a route needs.** `web/src/lib/nav.ts` holds the
sections as data, and `capabilityFor` is the single answer to "may this be
rendered" — the sidebar filters from it and the router gates from it. The
arrangement it replaced spelled the same facts out per case in the router; they
agreed, nothing made them, and Overview had already been missed. None of it is
access control: the server authorises every call again. A route that renders
its chrome and then fails every fetch is a worse answer than a sentence.

**Routing is thirty lines rather than a dependency.** There are no nested
layouts, no loaders and no route-level data — a section, an optional record
within it, and the back button working. Real paths rather than a hash, which
works because `staticHandler` already serves `index.html` for any path it has
no file for.

**A plain `<select>`, not Radix's.** That component exists to build a listbox
out of divs so it can be styled and animated; it costs roughly twenty kilobytes,
needs a portal, and reimplements typeahead, scrolling and touch behaviour the
platform already has. Nothing here asks a select to do anything a native one
does not, and the native one is what a phone renders as a proper picker. The
cost is that the popup is the browser's, so it has to be coloured through the
two things the browser reads — the options' own colours and the control's
background, which Chromium copies onto the popup. Both halves are named, in
`index.css` and in `native-select.tsx`, because naming one is the bug.

**The catalogue list is memoised, not virtualised.** A held list re-renders on
every keystroke during the search debounce, which is what memoising fixes.
Windowing was considered and left out: reaching five hundred rows means pressing
Show more a dozen times, the realistic ceiling is a screenful or two, and
against a few milliseconds of render it costs a fixed-height scroll container
fighting the page's own scroll, rows that are not in the DOM for browser find or
a screen reader, and either a dependency or a hand-rolled implementation with
its own bugs.

Two rules about the UI itself are enforced by tests in `web/src/index.test.ts`
rather than by memory: every colour token clears WCAG contrast on every surface
it is drawn on, and nothing that reports state may carry an `animate-` class —
a health pill that fades in or a lifecycle node that slides into place reads as
a state that just changed, and none of them is.

`make verify-deps` exists because `modernc.org/sqlite` requires
`modernc.org/libc` at the exact version in its own `go.mod`. A mismatch fails
at runtime rather than at build time, so it is checked in CI.

The container's data directory is bind-mounted at `./data`. It is an ordinary
directory owned by whoever runs the container, so `go build ./...` reads it
like any other and finds no packages in it.

## Deployment

Primary target is a Linux VM with systemd. [`deploy/mcpd.service`](../deploy/mcpd.service)
is a hardened unit: dedicated user, `ProtectSystem=strict`, all capabilities
dropped except `CAP_NET_BIND_SERVICE`, and a syscall filter.

### The container

**One mount, and it is generated.** `./data` holds `config.yaml`, the database,
TLS material and out-of-process plugins. `docker compose up` against an empty
directory produces a working host: the entrypoint runs `mcpd -init` when there
is no config, which writes the file and generates a bearer token and the key
that encrypts stored credentials.

**Generation happens exactly once, and that is the load-bearing part.**
`secret_key_ref: env:MCPD_SECRET_KEY` is what encrypts every credential typed
into the dashboard, so a restart that generated a second key would make every
one of them undecryptable — and `Store.Get` drops what it cannot decrypt, so
nothing would say so beyond a credential quietly no longer being there. Three
things hold the line. `mcpd -init` refuses outright to overwrite an existing
`config.yaml` or `.env`; it declines to write a key at all when the environment
already supplies one, rather than writing a second that would take over the day
the environment stopped; and the entrypoint calls it only when neither file
exists, refusing rather than generating a config beside secrets it did not
write.

**Alpine, not distroless, and the trade is stated rather than waved away.**
What is lost is real: a shell in the image gives a remote code execution more
to work with, and there is a musl userland and a package manager to keep
patched. What is not lost is what actually hardens the container — read-only
root filesystem, `cap_drop: ALL`, `no-new-privileges`, a nonroot user, `/tmp`
on tmpfs, and a static CGO-free binary. What is bought is the two things
distroless made impossible, which were the whole of the complaint: nothing can
run before the binary, so the config had to be hand-authored and bind-mounted,
and the volume had to be pre-chowned to uid 65532 — which left an operator
with a data directory their own account could not read.

**The container runs as the host user's uid**, `${UID:-1000}:${GID:-1000}`,
rather than chowning the mount from an entrypoint. Chowning a bind mount needs
the container to start as root holding `CAP_CHOWN`, `CAP_SETUID` and
`CAP_SETGID`, which means handing back three of the capabilities dropped above
to solve a problem that has a solution needing none of them.

**`config.yaml` is no longer mounted read-only**, because it lives in the one
writable mount. That is a real change and worth naming: what kept mcpd from
rewriting an operator's YAML was never only the mount flag — there is no code
that writes it, and there is no reason to add one (see *plugin overrides*
below). Under systemd it is still under `ProtectSystem=strict`.

**The generated file is five lines**, and a test asserts it line for line. It
is the claim the whole arrangement rests on: anything else appearing there is a
key that could have been a recorded, attributed setting and was not.

**Behind a reverse proxy, mcpd should not serve TLS itself.** The ordinary
shape is an FQDN with Caddy, nginx or Cloudflare terminating TLS and forwarding
plain HTTP, and the right setting for that is `tls.mode: off`; `self-signed` is
for reaching mcpd directly, where the alternative is a browser warning on every
visit. Both are settings, on the Settings page. Set the public address to the
one people actually type. It is not cosmetic: mcpd is reached over plain HTTP
in that shape, so `r.TLS` is nil and the configured scheme is the only way it
can know the session cookie needs `Secure`. `X-Forwarded-Proto` deliberately does not count — a header is set by
whoever is talking to this process, and nothing here can tell a proxy's from a
caller's.

## Logs somebody can use from a support call

The hard case is a machine nobody here can reach, running a version nobody here
can reproduce against. Two things follow from that.

**The correlation ID has to actually appear.** `Correlate` puts a tagged logger
in the context and returns the ID to the caller in a header and in every error
body, so it is the one thing a person can quote back — and it found nothing in
the logs, because the request-scoped logger was used at five call sites out of
several hundred. `contextHandler` now reads the ID off the context and puts it
on any record written with one, and the log calls that had a `ctx` in scope
were converted to the `*Context` forms.

That conversion is the whole fix rather than half of it: slog hands a handler
`context.Background()` for the plain `Error(...)` form, so no handler can
recover what was not passed. Which is why the convention in
[CLAUDE.md](../CLAUDE.md) is a convention rather than something the
infrastructure can enforce.

**Debug is for the questions a support call asks.** There were six debug lines
in the whole project, so telling a customer to raise the level bought them
almost nothing. The ones that matter are what was asked for, what was decided
and what the upstream said: a tool call and its capability, a mutation being
proposed, one Observium API call with its status and count, one database query
with its table and row count. Never a response body, never a query's arguments
— those are the customer's estate, and the point of the line is the code path.

**Warnings and errors become Sentry breadcrumbs.** A stack trace says where a
panic landed; the run-up says what it was doing. Breadcrumbs rather than events
is deliberate: this project logs an error when an upstream is unreachable or a
proposal is refused, which are things a working system does, and sending each
as an event would fill a collector with normal behaviour. As breadcrumbs they
cost nothing until something actually panics. Their attributes are an
allow-list — the keys that identify a code path, not the ones that name
equipment — and they are scrubbed like everything else.

None of this needs a collector. The logs are the record that always exists;
breadcrumbs are what a collector gets to see if an operator configured one.

## Crash reporting, and whose machine this is

mcpd is deployed onto a customer's network, manages their equipment and holds
their credentials. A crash report is therefore the only thing this process
sends anywhere the operator did not choose, and that shapes every decision in
`internal/observability/errors.go`.

**Off unless somebody turns it on.** No DSN means no client, no goroutine and
no network calls — not a client pointed at nowhere. `NewErrorReporter` returns
`nil, nil`, and the nil reporter is valid: every method on it is safe. That
matters more here than for metrics, because the call sites are panic handlers
and a forgotten check in one would turn a recovered panic into a second.

**One gate, not many call sites.** Everything is scrubbed in `BeforeSend`,
which is the single point every event passes through however it was raised and
whichever SDK integration added it. Scrubbing at the call sites would mean
every future caller remembering.

**Nothing identifies the machine unless asked.** Sentry fills `ServerName` with
`os.Hostname()`, which on a customer deployment is the customer. It is replaced
by a label an operator types — empty by default — and the empty case sends a
single space, because an empty string is read by the SDK as "not configured"
and it substitutes the hostname. `scrubEvent` forces the value from
configuration rather than reading what is on the event: a gate that trusts its
input is not a gate.

**Messages are a separate decision from crashes.** A Go stack trace carries
function names and line numbers and *not* argument values, so it is
structurally incapable of naming a device. The error sentences are where a
customer's estate lives — this project writes them to name upstreams, hosts and
tables, which is right for a log on their own machine. So `errors.include_messages`
is off by default: the trace and the error type travel, the sentence does not.

Absolute paths go entirely. `abs_path` is the layout of whatever machine
compiled the binary, so it carries a build user's home directory and says
nothing the repository-relative filename does not.

**The DSN is a setting, never a build-time constant.** It lives in the database
with everything else, so an operator can see where their crashes go and stop
them, and so a customer can point mcpd at their own collector rather than ours.
The cost is that a crash before the settings store opens is not reported; the
alternative is a second authority for one key and a file that could switch on
sending data off the machine without the dashboard showing it.

`Scrub` in `scrub.go` redacts addresses, credentials, tokens, MACs, emails and
hostnames while deliberately protecting Go import paths and file names — a
report with its own identifiers removed is one nobody can act on, and an import
path and a domain name are the same shape.

## Plugins are not architecture

What an integration does belongs with the integration. Each has its own
document when it needs one — [cnmaestro.md](cnmaestro.md),
[observium.md](observium.md), [graylog.md](graylog.md) and
[extremecloudiq.md](extremecloudiq.md) — because the API a plugin talks to
changes on someone else's schedule, and mixing that into the host's design
makes both harder to read.

Those documents are worth reading as a set before writing a fifth integration.
They record the same *kinds* of surprise in four different vendors: where
pagination hides, what an empty result is spelled as, which status code means
two things, what unit a timestamp is in, and what the API will not give you at
all. A plugin author who expects those categories finds them faster than one
discovering that such categories exist.

They also disagree with each other in an instructive way. The read-only
guarantee is a method check in `observium`, an allow-list in `graylog` because
its searches are POSTs, and an allow-list in `extremecloudiq` even though every
read there is a GET — because that API answers `GET
/account/viq/default-device-password`, and a guarantee that permits a
credential dump is not the guarantee it says it is. The shape follows from the
API rather than from a house style.

What is architectural is the contract every plugin meets.

**A type, and its instances.** A plugin *type* is an integration the binary was
built with; an *instance* is one configured copy of it. The config key is the
instance name and `type` says what it is an instance of, defaulting to the key.
Two instances are two plugins as far as the host is concerned — two endpoints,
two entries in a credential's plugin list, two connectors, and operations that
say which one acted — because the name is already the identity everywhere
downstream. That is also why an instance argument on each tool was the wrong
design: access is granted per plugin, so a shared endpoint could not express
"this agent reaches one and not the other".

**Four things a plugin can declare.**

| | what it is |
|---|---|
| Tool | an action a model chooses and reasons about choosing |
| Mutation | a write, which becomes propose/approve rather than a tool that writes |
| Resource | reference material read by address, kept out of the tool catalogue |
| Prompt | a named way of asking something useful; returns text, performs nothing |

All four pass the same authorization gate. A resource that skipped it would be
a way around per-plugin scoping, and a prompt that acted would be a tool
wearing a name that hides it from every check tools go through.

A tool may raise its capability above read — for the read that is not merely a
read, where seeing something is itself the privilege — and may declare a rate
limit, per tool rather than per plugin, because the expensive call is usually
one endpoint rather than an integration.

**A rate limit refuses; it does not queue.** It used to wait for a turn, which
looks like the polite thing to do and is the wrong thing here. The caller is a
model with a deadline: a queued call arrives at the front having spent most of
the budget it needed to do the work, and every caller behind it holds a
goroutine and a context for as long as the queue is. Refusing immediately turns
a hidden stall into a fact the model can act on, so the error says how long to
wait and in what units. A refusal does not consume the turn it was refused,
which is what keeps a burst of rejections from pushing everybody back.

**A read tool's result is evidence a model acts on, which is what decides
whether it may be reused.** Caching a plugin read is not only a freshness
question. A stale device state does not merely look out of date to a person; it
is a premise a model reasons from and then proposes a change against. So where
a plugin caches, three rules hold, and they are the opposite of the
catalogue's.

Nothing stale is ever served. The catalogue cache serves an expired answer
while a refresh runs behind it, because a browse page rendering slightly behind
beats one that does not render. Here the reader is about to act, and "this is
what the estate looked like a while ago" is not a safer answer than waiting.

What may be reused at all is an allow-list, so an endpoint nobody has thought
about is fetched every time. And a key is built from the upstream request that
will actually be made — the endpoint and the fully resolved query — never from
the arguments a tool was called with. That is what makes a shared cache
defensible: every caller of one plugin instance reaches the same upstream with
the same credential, so two callers producing one key produce byte-identical
requests and therefore identical responses. A plugin whose request varies by
caller — a per-user token, a header derived from the principal — must put the
caller in the key or not cache at all. A cache keyed without the caller, where
the response depends on them, is an access-control hole rather than a
performance decision.

**Settings belong to the plugin, resolution belongs to the host.** A type
declares its fields; the host namespaces them per instance, validates them,
encrypts the secrets, renders the form, and hands back resolved values. A
plugin never reads a file or an environment variable. Values resolve store,
then file, then default — the store winning because a value changed in the
dashboard has to beat the one the host started with.

A plugin whose credentials are entered in the dashboard cannot refuse to start
without them: a host that will not start is a dashboard nobody can open to
enter them. Structure is validated at construction, credentials at `Start`, so
an unconfigured instance mounts, shows its form, and reports what is missing.

**Instances come from two places.** The configuration file, and the settings
store where the dashboard writes them. The store layers over the file, and an
instance knows which it came from, because the two are removed differently.

**The dashboard can remove a file-declared plugin, and mcpd never touches the
file.** There is no code anywhere that writes `config.yaml`, and under systemd
`ProtectSystem=strict` would refuse it anyway. The container used to enforce it
too, by mounting the file read-only; it now lives in the writable data
directory, so the guarantee there rests on the absence of the code rather than
on the mount. Nor should it — rewriting hand-authored YAML destroys
comments, ordering and anchors, and in any deployment provisioned by
configuration management the next deploy would put the entry back. "Remove it
from the file instead" was therefore an instruction that a great many operators
could not carry out, which made it a dead end rather than an answer.

So a removal is a row in `plugin_overrides` saying the file's declaration for
that name is ignored, and every read of the instance list applies it. It
survives a restart because it is in the database and the file is not consulted
about it; it beats a redeploy for the same reason. The same row carries an
override of `enabled`, which is the identical dead end one step smaller.

**Keyed on the name, not on the declaration.** Pinning the override to a hash
of the file entry — the way `descriptor_hash` pins a tool approval — would mean
that editing the entry silently resurrects the plugin, which is the failure
this exists to prevent. A tool approval is a statement about a descriptor; a
removal is a statement about a name.

**Reversible, and reversible to the file.** Restoring forgets the override
entirely, so what comes back is whatever the file declares now rather than a
copy of what it declared then. The settings are kept across a removal, because
a restore that came back without the credentials somebody typed in would be a
restore in name only — which is also the difference from removing a
dashboard-defined instance, where the settings do go, so that a name reused
later cannot silently inherit them.

**Removing one is an administrative act and is audited.** It overrides the
deployment's own configuration, so it appends to the hash-chained trail inside
the transaction that performed it, like importing a server or classifying a
tool, rather than landing in the settings history. `required: true` is the
deployment saying the host should not run without an integration; removing one
of those is allowed and takes an explicit acknowledgement, because it should
not be a side effect of confirming something else. Every override is named in
the log at startup for the same reason `shadowedNames` is: a plugin the file
says is enabled and that this host is not serving is hard to diagnose from
outside.

**A removal outlives the declaration it overrode.** An operator who removes a
plugin here and later deletes the entry from their YAML leaves a row matching
nothing. Those are kept rather than discarded — one start against a truncated
or missing file would otherwise forget every removal and resurrect all of them
on the next good deploy — and are reported to the dashboard so they can be
forgotten deliberately.

Adding an instance records intent; it does not mount. A plugin is built once,
at startup, from the settings it had then, so the dashboard says a restart is
needed rather than showing an instance whose tools never appear.


## Watching the log

The Logs page shows what the host is doing as it does it. `internal/observability`
keeps a bounded ring of recent lines and fans new ones out to whoever is
watching; `internal/admin` serves them.

**The copy is taken through a handler of its own, not off the destination's
bytes.** Two things follow from that and neither is incidental. The dashboard
gets JSON whatever format an operator has chosen for the file — those are
different audiences and only one of them can be asked to cope with a change.
And a handler built from the same options carries the same level filtering and
the same redaction, so a value withheld from the file cannot reach a browser by
this route. The cost is that a streamed line is rendered twice, which is why
the tap is nil unless a host asks for it.

**A watcher is dropped, never waited for.** The alternative is a goroutine
blocked inside `Handle` holding the writer's lock, with every other goroutine
that wants to log queued behind a browser on a slow connection. What a watcher
missed is counted and sent to them, because a gap in a log with nothing marking
it reads as "nothing happened".

**Server-sent events, not a WebSocket.** Nothing travels upwards. A socket
would buy a direction that is not used, at the price of a dependency this build
does not otherwise have, and `EventSource` reconnects on its own — which is
most of what a hand-written client would have to get right.

**Admin, not read.** The log carries every request this host served, which
systems were called and by whom. That is a wider view than any one account's
own work.

Redaction is by attribute key — `api_key`, `token`, `password` and the rest of
`redactedKeys`, matched on a normalised form so `API-Key` and `apiKey` are the
same key. A secret written into the *text* of a message rather than given a key
of its own is not caught, which is why credentials are logged by fingerprint.
The page says so rather than implying a guarantee it does not have.
