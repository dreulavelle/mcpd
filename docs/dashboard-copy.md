# How the dashboard speaks

The people running mcpd are ops managers, not the people who wrote it. Every
string they read is written for them.

## Six rules

1. **What happened, then what to do.** In that order. Two sentences is usually
   the limit; if there is nothing to do, one is the whole message.
2. **Plain words.** "Connection" not "tunnel client"; "OpenAI's tunnel service"
   not "control plane"; "working" not "healthy" or "mounted". The nouns the UI
   itself teaches -- plugin, connector, tunnel, approval, rule, key, role,
   group -- are fine.
3. **No evidence in a sentence.** Error codes, HTTP statuses, header names,
   file paths, API routes and log lines go under "Technical details" or into a
   block meant for copying. Never into prose.
4. **No justifications.** "Because…", "which is the difference between…", "so
   that…" belong in a code comment. A lede is one short sentence saying what
   the page is.
5. **Sentence case, active voice, present tense.** Buttons name the action
   ("Save changes"). An empty state says what to do next, in one line.
6. **mcpd is not a character.** "mcpd is trying again" is a fact. "mcpd never
   attaches one because the email happens to match" is a personality.

Text somebody else wrote -- a plugin's health message, what a remote server
said -- is quoted, not run in: `graylog is having trouble. It said: “…”`. Run
into the line, it reads as the dashboard's own words.

## Where the words live

A failure carries three separate facts and they stay separate: the **sentence**
a person reads (`Status.Message`), the **evidence** behind it (`Status.Detail`,
`Status.Code`), and any **log line** the thing itself wrote (`Status.Trouble`).
Anything that renders one of the last two in prose is a bug.

On the server, a response body is read by whoever made the request, so it says
something they can act on. `admin.writeProblem` passes through text with no
`package: ` prefix, strips the prefix for the short allowlist of packages that
write for a person, and answers anything else with the handler's own sentence
after logging the real error. Defaulting the other way meant naming every
package that might ever wrap an error, and that list was wrong the day it was
written.

On the client, `problemText` decides once whether the server's text is fit to
show. Everything but a 500 is: a 501 or 503 says what this host does not do
and a 502 quotes the far end. A 500 gets the page's own sentence and the
correlation id after it, which is the only thing somebody on a machine you
cannot reach can quote back.

## Six from the change that wrote this

| Was | Is |
| --- | --- |
| `It will not restart on its own. tunnel: OpenAI refused this account's key for this tunnel (tunnel_use_forbidden): the tunnel is not in this account's organisation. The Tunnels page names the account that owns it; assign the tunnel there, or forget it` | This connector has stopped. This tunnel belongs to a different ChatGPT organisation, so this account cannot use it. Move it to the account that owns it, or forget it. |
| `The echo connector says connected but its client has been reporting errors with nothing served: time=… level=WARN msg="poll failed; backing off" client_instance_id=…` | The echo connector is connected but nothing is getting through, and the connection keeps reporting errors. |
| `textable is degraded — /health timed out inside Textable (HTTP 408). …` | textable is having trouble. It said: “/health timed out inside Textable (HTTP 408). …” |
| `sqlite: begin: database is locked` | That change could not be saved. |
| `app: plugin "echo" has type "textable", which is enabled in configuration but not compiled into this binary` | This plugin's type is not in this build of mcpd, so it cannot start. Remove it, or run a build that includes it. |
| `rule "x" names plugin "y", which is not mounted here, so it matches nothing` | rule "x" names "y", which is not a system on this host. It matches nothing. |
