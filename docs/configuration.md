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

## Reaching an upstream behind your own certificate

An integration inside a company often points at an HTTPS address whose
certificate was issued by that company's own authority, or by the appliance
itself. Nothing in a public trust store has heard of either, so the connection
fails before a credential is ever sent:

```
graylog did not start with the new settings: 10.10.12.53 presented a
certificate this host does not trust. If it is your own — a company
authority, or the appliance's own certificate — add it under Settings,
Certificates, and every integration here will trust it.
```

**Settings → Certificates** is where that goes. Paste the certificate or pick
the file; mcpd parses it there and then, so an unreadable paste is refused
where you are looking rather than at a handshake weeks later. It takes effect
immediately — the plugins are remounted on the spot, and nothing needs
restarting.

Everything added there is trusted by every outbound connection this host makes,
**in addition to** the public authorities it already ships with. Two decisions
worth knowing about:

- **Additive, never a replacement.** An instance behind a company CA still
  reaches ordinary public endpoints, so the extras are appended to the system
  roots rather than standing in for them.
- **Host-wide rather than per-plugin.** Naming the certificate again on each
  integration that needs it would be a second step whose failure looks exactly
  like the problem being solved: the certificate is stored, and the handshake
  still fails. This is the arrangement a company root already has in an
  operating system's trust store.

Adding and removing are both recorded in the audit trail, because the
security-relevant fact is not the bytes — a certificate is public — but the
decision to believe them.

### What it can and cannot fix

A certificate is only useful here if a chain can be anchored on it. A
self-signed certificate straight off an appliance usually carries no extensions
at all, and that case works: nothing on it constrains it out of the role. One
that explicitly says `basicConstraints: CA:FALSE`, or names a key usage without
certificate signing, is a leaf that means it — the page says **cannot anchor a
chain** against it, and the authority that signed it is the one to add instead.

It also cannot help with the *other* certificate failure. If the address does
not appear on the certificate, the message says which names it does carry:
trusting it cannot cover a name it was not issued for, and the address is the
thing to change.

### Formats

PEM and DER both work; a `.crt` from a Windows authority is frequently DER
despite the extension, and it is converted on the way in. A PKCS#7 bundle
(`.p7b`, `.p7c`) is a container rather than a certificate, and mcpd says so
along with the one command that opens it:

```bash
openssl pkcs7 -print_certs -in bundle.p7b -out certificates.pem
```

One certificate per entry. A paste holding a whole chain is refused, naming
what it found, because a bundle in one row cannot say which certificate inside
it is the one expiring in six weeks — which is the question the page exists to
answer. A private key in the paste is refused outright.
