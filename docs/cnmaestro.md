# cnMaestro

Notes on Cambium's cnMaestro API, kept because they cost real effort to
establish and none of them is obvious from the reference. This is a plugin
document, not architecture — how mcpd is built is in
[architecture.md](architecture.md).

Reference: <https://docs.cloud.cambiumnetworks.com/api/latest/index.html>

## Authentication

OAuth 2.0 client credentials. Credentials come from the cnMaestro UI, under
API Clients, via **Download Credentials** — a client id and a client secret.

```
POST /api/v2/access/token
Authorization: Basic base64(client_id:client_secret)
Content-Type: application/x-www-form-urlencoded

grant_type=client_credentials
```

The credentials may instead be sent in the body as
`grant_type=client_credentials&client_id=…&client_secret=…`. The Basic header
is preferable: it keeps the secret out of anything that logs a request body.

The response is an ordinary token response with one addition:

```json
{
  "access_token": "…",
  "token_type": "bearer",
  "expires_in": 3600,
  "redirect_uri": "https://<region>.cloud.cambiumnetworks.com"
}
```

**`redirect_uri` is load-bearing.** Cloud accounts are regionally sharded, and
the token response names the host that subsequent calls must actually target.
Authenticating against `cloud.cambiumnetworks.com` and then calling it for data
is the classic first-integration failure: the token is valid and the calls go
to the wrong shard.

Tokens last an hour. Refresh ahead of expiry rather than on a 401, so
credential handling stays out of the error path where it would be tangled with
retries and rate limiting.

## Endpoints that execute code on a device

These are reachable with the same account-wide token as every read, and none of
them is needed to manage a network:

```
POST /devices/{mac}/cli                      arbitrary CLI execution
POST /cnwave60/devices/{mac}/remote_command  the 60 GHz equivalent
```

The second did not exist in API 5.0.1 and appeared in 6.3.0. That is the
argument for enforcing a deny-list in code rather than in a design document: a
list that lives in prose does not survive the API growing a new way to run
commands.

Lower individually but unbounded together, and equally unnecessary:

```
POST /devices/{mac}/ping
POST /devices/{mac}/traceroute
POST /devices/{mac}/pull_config
POST /devices/{mac}/wifi_perf
POST /cnwave60/devices/{mac}/ping
POST /cnwave60/devices/{mac}/iperf
POST /cnwave60/devices/{mac}/links/{id}/iperf
POST /cnwave60/devices/{mac}/topology_scan
```

## Pagination

Follow continuation tokens. Offset pagination is deprecated upstream and
removed in 6.4.0, and it is unsound in the meantime: rows shift between pages
as devices come and go, so a walk over a changing estate silently skips and
repeats.

## managed_account

Attach it to every request. Omitting it means different things depending on
whether the request names a network, which makes "leave it off unless needed" a
rule with an exception nobody remembers.

## Unverified

**Whether `PUT /devices/{mac}` merges or replaces the `overrides` object is
undocumented.** Resending every override that was read is correct under either
behaviour, and is what the previous implementation did. Confirm against real
hardware before the first production write; a replace-semantics API silently
discards settings a merge-semantics client assumed it was leaving alone.

Everything here was established against the published 6.3.0 specification and
a fake controller. None of it has been exercised against a live controller.
