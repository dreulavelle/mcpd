# Backup and restore

One instance, in one encrypted file. **Settings → Backup & Restore**, or the
API below.

## What is in a backup

| | |
|---|---|
| `mcpd.db` | Settings, accounts, groups, grants, API keys, ChatGPT accounts, approvals, and the audit trail. Restored. |
| `tls/` | This host's own certificate authority, so a restored instance keeps the identity clients were told to trust. Restored. |
| `config.yaml` | Carried so the archive is a complete record. **Not restored.** |
| The encryption key | **Not in the archive at all.** See below. |

`config.yaml` is left alone deliberately. It holds where the database is and
what to bind — facts about the machine, not about the instance — and a path
from the machine the backup came from is at best wrong on the machine it lands
on. The target keeps its own.

## The encryption key

Secrets in the database are encrypted with the key at `secret_key_ref`, and
that key is not in the backup. It is what makes a stolen archive useless on its
own, and it is the same reason the key is not in the database.

A restore therefore needs a host using **the same key**. The page shows a
fingerprint of the current one; an archive written under a different key is
refused with a message saying so, rather than restoring into a host that starts
and then cannot read a single credential it holds.

So when moving to another machine, copy the key across first — it is one line
in `.env`:

```bash
# on the old machine
grep MCPD_SECRET_KEY data/.env
# on the new one, put the same value in data/.env, then start mcpd
```

Then restore. The fingerprints on both hosts will match, and everything the
archive holds opens.

## The passphrase

The archive is encrypted with a passphrase you choose, at least 12 characters.
There is no way into the file without it and no way to recover it — keep it
with the backup.

It is not optional. The archive holds every account, group, grant and audit
entry this host has, and a plain tarball of that is not something to leave in a
bucket.

## Taking one

**Settings → Backup & Restore → Back up.** Enter a passphrase twice and
download. The file is named `mcpd-<timestamp>.mcpdbak`.

The database is snapshotted with SQLite's `VACUUM INTO`, so it is a consistent
copy taken without blocking anything; mcpd keeps serving throughout.

## Restoring

**Settings → Backup & Restore → Restore.** Choose the archive, enter its
passphrase, and press **Restore and restart**.

The archive is checked before anything is replaced: every file against the hash
the manifest records, the database against SQLite's own integrity check, the
schema against what this build knows, and the key fingerprint against this
host's. If any of that fails, the restore is refused and the instance is
exactly as it was.

If it passes, mcpd restarts itself, and the next start moves the files into
place before it opens anything — which is the only moment it is safe, because a
running mcpd holds the database open.

The restart is not a second thing to press, and that is deliberate. A staged
restore applies on the next start of **any** kind — a reboot, a `compose up`, a
crash loop — so one left waiting is a change that fires later, at a moment
nobody has connected to a restore. Applying it immediately keeps the act and
its effect in the same place.

If the host cannot restart itself — nothing supervising it, or a restart
already under way — the checked archive is kept rather than thrown away, and
the page says it will apply on the next start. That is the one case where the
staged state is visible, and it can be cancelled from the same page.

### The way back

The database being replaced is kept, under `superseded/<timestamp>/` in the
data directory, along with any TLS material it replaced. Nothing removes it.

It is the only undo. A restore is the one operation here that destroys the
current instance, and now that it applies immediately there is no window in
which to change your mind — so if you restored the wrong archive, that
directory is what you put back:

```bash
docker compose stop mcpd
cp data/superseded/<timestamp>/mcpd.db data/mcpd.db
rm -f data/mcpd.db-wal data/mcpd.db-shm
docker compose start mcpd
```

The last three are kept and older ones are removed at startup, so a run of
restores while you work out which archive is the right one does not fill the
volume. Delete them yourself whenever you like; nothing depends on them once
you are satisfied the restore was right.

## Migrating to another machine

1. Take a backup on the old host.
2. Put the old host's `MCPD_SECRET_KEY` in the new host's `.env`.
3. Start the new host, sign in, and restore the archive. It restarts itself.

The new host keeps its own `config.yaml` — its own paths and ports — and gains
everything else.

## The `-backup` flag

`mcpd -backup <path>` is a different thing and still exists:

```bash
mcpd -config /var/lib/mcpd/config.yaml -backup /backups/
```

It writes a plain, unencrypted snapshot of the database and nothing else. It
takes no passphrase and needs no dashboard, which makes it the right shape for
a cron entry on a host you already trust. It is not a whole instance and it
will not restore one — for moving a host, or for a copy that leaves the
machine, use the archive.

## API

All four routes need `admin`.

| | |
|---|---|
| `GET /api/backup` | What an archive taken now would hold, and any staged restore. |
| `POST /api/backup` | `{"passphrase": "..."}` → the archive as a download. |
| `POST /api/backup/restore` | Multipart: `passphrase`, then `archive`. Checks it and restarts. |
| `DELETE /api/backup/restore` | Discards a staged restore. |

The passphrase part must come before the file: the upload is streamed straight
into the decryption rather than buffered anywhere.

`POST /api/backup/restore` answers `202` with a `status` of either `restoring`
— the host is going down to apply it — or `staged`, meaning it was checked but
the restart could not be asked for and it will apply on the next start. The two
are worth telling apart: only the first is followed by a reconnect.
