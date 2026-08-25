# Configuration

How mcpd is configured, and where its secrets live.


Almost all of it is in the dashboard, and the config file is five lines. A
generated one looks like this in full:

```yaml
server:
  listen: "127.0.0.1:9080"
  frontend_listen: "127.0.0.1:9090"

storage:
  path: /var/lib/mcpd/mcpd.db

secret_key_ref: env:MCPD_SECRET_KEY
```

Those four cannot live in the database. `storage.path` is where the database
is, and it cannot say where it is from inside itself. `secret_key_ref` names
the key everything secret in the database is encrypted under, and a lock does
not hold its own key. The two bind addresses are a judgement rather than a
limit: a bad one stored in the database would lock you out with no page left to
fix it on, so the file is the way back in.

Everything else — the address to advertise, TLS, timeouts, logging, approval
policy, sessions, ChatGPT — is a setting on the Settings page. That is not
tidiness. Editing a file leaves no record of who changed what, or what it was
before; a settings change records both, and the dashboard can show you.

Upgrading from a version that kept these in the file needs no hand-editing.
The first start imports what your file sets, once, records it against
`system:config-import`, and ignores those keys from then on. Any that are left
behind and disagree with what mcpd is running get named at startup, so the two
never quietly differ. See [`configs/example.yaml`](configs/example.yaml) for
what a fuller file can still declare.

## Where the secrets are

There are no plaintext passwords on disk, and there never were. Account
passwords are bcrypt in the database. Plugin credentials, SSO client secrets
and the tunnel's API key are encrypted at rest. API keys are stored as digests
— mcpd cannot read one back, only check one. Static tokens in the file are
*references* (`env:`, `credential:` for systemd `LoadCredential`, or `file:`),
resolved at startup, so a config file that never holds a token cannot leak one.

The one secret in the clear is `MCPD_SECRET_KEY`, in `data/.env` at mode 600.
It is the key everything above is encrypted under, so it is the one thing that
cannot itself be encrypted by the system it unlocks. Guard that file; a stolen
copy of the database without it is unreadable.

**Scoping is per credential.** This agent reaches `/mcp/echo` and nothing else
— every other endpoint returns 404, so it cannot discover what else is
deployed:

```yaml
auth:
  static_tokens:
    - id: chatgpt-echo
      secret_ref: env:MCPD_TOKEN_CHATGPT
      principal: svc:chatgpt
      role: user
      plugins: [echo]
```
