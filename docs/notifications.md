# Notifications

Settings → Diagnostics → **Notifications**.

Off until you fill in an address. mcpd runs on your hardware, and a host that
started talking to an outside service because it was upgraded would be doing
something nobody agreed to — the same reason crash reporting is off by default.

## What it will never do

**It never asks you to approve anything.** Approval happens where the work is:
below the inline ceiling a client's own confirmation settles a change, and
above it the assistant shows the change in full and is told explicitly, still
in the conversation. A message saying *something is waiting, open the
dashboard* would build exactly the path that design avoids, so mcpd does not
send one. Notifying about the pending queue was considered and left out.

Every event is a statement about something that already happened.

## What it sends

| Event | When |
|---|---|
| `mcpservers.tools_changed` | A remote server added, changed or withdrew a tool. Anything added or changed has just stopped being served until it is approved again. |
| `mcpservers.discovery_failed` | A scheduled check of what a server offers failed, so its tool list is no longer being confirmed. Only from the schedule — somebody who pressed **Discover** is already looking at the failure. |
| `approvals.bypass_opened` | Somebody stopped the asking. This is the one the feature earns its place with: a window opened and forgotten is what the banner and this both exist to prevent, and a message is the half that reaches somebody who has closed the tab. |
| `notifications.test` | You pressed **Send a test**. |

## Where it sends

Three shapes, because the receivers people actually have want different ones.

**`slack`** — one `text` field. Also fits Mattermost and Discord's
Slack-compatible endpoint. Blocks are deliberately not used: a payload that
renders beautifully in one of them fails to render at all in another.

**`ntfy`** — ntfy's publishing format, with the topic in the body. A warning is
sent at priority 4 rather than 5; deciding on your behalf that mcpd should
pierce your phone's quiet hours is not this host's call.

**`json`** — mcpd's own event, carrying the `kind` a receiving system routes on:

```json
{
  "kind": "approvals.bypass_opened",
  "title": "Changes are being approved without asking anyone",
  "text": "user:someone opened a window covering every plugin, up to medium risk, until 14:00 UTC. Reason: migrating the edge switches",
  "severity": "warning",
  "source": "mcpd",
  "at": "2026-08-30T13:00:00Z"
}
```

The address is stored as a **secret**, because a Slack or Discord webhook URL
is a bearer credential wearing an address's clothes — anybody holding it can
post as you. A separate token field is sent as `Authorization: Bearer` for a
receiver that wants one; ntfy may, Slack and Discord do not.

## Delivery

A notification is a courtesy. Nothing this host does fails, or waits, because
one could not be delivered:

- Events are queued and delivered by a worker, never on the path of the tool
  call, discovery or approval that raised them.
- The queue is bounded. A receiver that has stopped answering costs a dropped
  message rather than a growing backlog, and the drops are counted and reported
  at intervals so a dead receiver cannot fill the log either.
- A receiver's error **status** is logged; its body is not. An error page can
  carry anything, and this host has no business copying it into a log.

## Check the address

Settings → Diagnostics → **Send a test**.

This exists because the failure it catches is silent by construction.
Everything else is queued and cannot fail a caller, so a mistyped address costs
nothing at the moment it is typed and everything at the moment you needed to
hear from this host. The test sends synchronously and reports what the receiver
said — a 404 means the webhook is gone, a 403 means the token is wrong, and
"it did not work" would mean neither.

If the test succeeds and nothing arrives, the address answered but delivered
nowhere: check the topic, or the channel the webhook posts to.
