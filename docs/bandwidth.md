# Bandwidth

Calls, conferences, recordings, messages and toll-free verification on one
Bandwidth account. Read-only.

## The credential

Bandwidth issues API credentials as an **OAuth2 client id and secret**, made in
the Bandwidth console under API credentials. mcpd exchanges them for a
short-lived bearer token, caches it, and renews it about a minute before it
lapses. Nothing here ever sends the secret to a product API.

Three things about that credential are worth knowing before you make one,
because none of them can be changed afterwards.

**Its accounts are fixed at creation.** A credential is scoped to one or more
account numbers. One mcpd instance reads one account, so if you want all four
of yours, configure the integration four times — the instance name is then what
answers "which account did that come from", rather than a parameter somebody
forgot to pass. At startup mcpd reads the accounts out of the token's own
claims and refuses to run if the account you configured is not among them,
naming both sides. That mistake otherwise shows up as a 404 on every call.

**Its roles are fixed at creation too**, and Bandwidth's roles are *not* split
into read and write. "Campaign management" grants creating a campaign as well
as reading one; "Ordering" grants placing an order. There is no role that
grants looking without touching. So a credential scoped for the reads below can
also write, and the read-only guarantee lives in this plugin's transport rather
than in the credential. See below.

**Its secret has an expiry you choose**, and the API does not report it.
Nothing here can warn you before it lapses, so write the date down. An expired
secret is refused exactly like a wrong one; a new secret is issued from the
same credential and the client id does not change.

### Which roles

Bandwidth answers a missing role with a bare 403 that does not say which of the
thirteen is missing. This plugin guesses, from the path, and says so — a guess
that turns a search among thirteen into one edit. Start with the fewest that
cover what you want to read:

| To read | Role |
|---|---|
| calls, conferences, recordings, transcriptions | Basic access |
| account call statistics | Reporting |
| messages and media | Messaging insights |
| toll-free verification | Campaign management |
| endpoints | Configuration |
| number lookup results | TN lookup |

## What it reads

Fourteen tools across three of Bandwidth's hosts, which serve one product each
rather than one API under one root:

| Host | What |
|---|---|
| `voice.bandwidth.com` | calls, conferences, recordings, transcriptions, statistics |
| `messaging.bandwidth.com` | messages, stored media |
| `api.bandwidth.com` | toll-free verification, endpoints, number lookup |

Two absences are deliberate.

**No audio, ever.** Bandwidth will hand back the bytes of a recording or an MMS
attachment, and those endpoints are left off the allow-list. A media file is
not a read anybody asked for, and putting one into a model's context is a
mistake that is expensive before it is useless.

**No message bodies**, because Bandwidth does not store them. `search_messages`
answers "did that text arrive" from status and error code, which is the
question people actually have.

## The read-only guarantee

Every request this plugin makes is checked against a named allow-list in
`transport.go` before it leaves. A path that is not on it is refused here, not
upstream — including a redirect the plugin did not write, and including a bug
in this package that builds the wrong URL.

This matters more here than it does for an integration whose credential can be
scoped to reads, because Bandwidth's cannot. `POST /calls` places a real
telephone call with the same credential these reads use. The allow-list is what
stands between those two facts, which is why adding a read means adding a line
to it on purpose.

## Two Bandwidth accounts

Add the integration twice, with the same client id and secret and a different
account id. Nothing is shared between the instances: separate clients, separate
tokens, separate health.

## What is not here yet

The **Dashboard API** — phone number inventory, sites, SIP peers, orders,
port-ins, E911 locations and notification recipients, 10DLC campaigns and
brands, and applications. It is a second phase rather than a second product:
Bandwidth serves it from the same gateway at `api.bandwidth.com/api/v2`, under
the **same** OAuth2 credential, so no new secret is needed. What it does need
is an XML decoder, because that half of Bandwidth speaks XML where this half
speaks JSON.
