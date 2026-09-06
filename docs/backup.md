# Backup and restore

One instance, in one encrypted file. **Settings → Backup & Restore**, or the
API below.

## What is in a backup

| | |
|---|---|
| `mcpd.db` | Settings, accounts, groups, grants, API keys, ChatGPT accounts, approvals, and the audit trail. Restored. |
| `tls/` | This host's own certificate authority, so a restored instance keeps the identity clients were told to trust. Restored. |
| `plugins/` | The out-of-process plugins you installed by hand, with their executable bit. Restored. |
| `config.yaml` | Carried so the archive is a complete record. **Not restored.** |
| The encryption key | **Not in the archive at all.** See below. |

The plugins come along because a restored host without them is configured for
integrations that are not on the machine. They are restored **file by file**:
a plugin this host has and the archive does not is left where it is, so
restoring an older archive does not remove something you installed last week.

Symlinks in the plugins directory are left out, and the log names them. Very
large plugin directories are refused with a message naming the directory —
a backup mcpd cannot restore is worse than no backup.

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

## Sending backups somewhere on a schedule

A backup you have to remember to take is one that stops happening. **Settings →
Backup & Restore → Destinations** is where mcpd sends one for you.

A run takes **one** archive and sends that same archive to every destination
that is switched on. It never takes one per destination: three snapshots would
be three different moments pretending to be one backup.

### Destinations

| | |
|---|---|
| **A folder on this machine** | Any directory mcpd can write to, including an SMB or NFS share the host already mounts. It is also where you point rclone or restic if you would rather they did the moving. |
| **SFTP** | The one nearly every NAS has. See the Synology walkthrough below. |
| **S3** | AWS, Cloudflare R2, Backblaze B2, Wasabi, MinIO, Hetzner, DigitalOcean Spaces — one form for all of them. |
| **WebDAV** | Synology's WebDAV Server package, Nextcloud, a Hetzner Storage Box. |

Add as many as you like. Each has its own credential and its own retention: a
NAS with room for a year and a bucket you pay for by the gigabyte are not the
same policy.

**Test connection** does what it says and a little more — it reaches the
destination, lists it, and writes and removes a small file. Listing alone
proves it is readable, which is not the question: a share mounted read-only
lists perfectly and fails at four in the morning.

A folder on this machine cannot be mcpd's own data directory, anything inside
it, or anything containing it. A backup written there is in the next backup,
which doubles every run, and it is lost with the disk it exists to survive.
mcpd will not create the directory either — a typo would otherwise make an
empty folder somewhere nobody meant and the backups would look like they were
working.

### A Synology NAS over SFTP

1. **Control Panel → File Services → FTP → SFTP**, tick **Enable SFTP
   service**. Note the port; it is 22 unless you changed it.
2. **Control Panel → User & Group**, create a user for mcpd. Give it read and
   write on one shared folder and nothing else, and no other application
   permissions.
3. Get the NAS's host key from a machine you trust:

   ```bash
   ssh-keyscan nas.example.com | ssh-keygen -lf -
   ```

   That prints a line per key, each starting `SHA256:`.
4. In mcpd, add an SFTP destination with the address, the user, the password or
   a private key, and the folder. Press **Test connection**. mcpd records the
   key the NAS presented and shows it to you.
5. **Compare the two.** If they match, switch the destination on.

That last step is the whole point of the exercise. From then on, a server
presenting a different key is refused and the backup is not sent — mcpd will
not quietly learn a new one, because anything that can put itself on that
address would get a complete copy of this instance if it could.

If you rebuild the NAS or replace its keys, clear the recorded key on the
destination and test again. It is deliberately a separate, deliberate act.

### S3, and its cousins

| | |
|---|---|
| Address | The service's host, without `https://` in front. `s3.amazonaws.com`, `<account>.r2.cloudflarestorage.com`, `s3.eu-central-003.backblazeb2.com`. |
| Region | `auto` for Cloudflare R2, which has one region and calls it that. mcpd fills that in if you leave the box empty. |
| Path-style addressing | On for MinIO and Backblaze B2. Off for AWS. |
| Folder | Optional. Everything mcpd writes goes under it. |

Give mcpd a credential scoped to one bucket, with **PutObject**, **ListBucket**
and **DeleteObject** and nothing else. It never reads an object back, so it
does not need **GetObject** — a key that cannot download is a key that cannot
be used to steal the archives it wrote.

**Backblaze B2 keeps versions.** On a bucket with versioning on, deleting a
file writes a hide marker and the old version stays and stays being charged
for. Retention will look like it is working and your bill will not agree. Add a
lifecycle rule on the bucket to remove hidden versions after a few days.

### How many are kept

Retention runs per destination, after a successful upload, from that
destination's own listing.

**Keep the last N** is the whole policy for most people. Under *Advanced* there
are also *keep the newest in each of the last N days / weeks / months*; the
rules are OR-ed, so anything any rule keeps is kept.

Three things it will never do:

- Delete the newest archive, whatever the numbers say.
- Touch a file this host did not write. Only names matching its own pattern
  *and carrying its own name* are considered, so a shared folder is safe — from
  somebody else's files and from another mcpd's backups alike.
- Prune on a listing it does not believe — one that came back empty, one
  missing the backup just uploaded, or one holding far fewer archives than last
  time. It records why and leaves everything alone.

The date it sorts by comes from the file's **name**, not its modified time. A
NAS with a wrong clock, a copy between buckets and a restore from the
destination's own snapshot all rewrite the second and none of them touches the
first.

### The schedule

Every day, or every week on a day you pick, at a time, in a time zone you
choose. The zone is stored rather than taken from the machine, so the time
means the same thing all year.

Avoid midnight, and anything between 01:00 and 03:00. Those hours do not exist
on the day the clocks go forward — midnight itself is the one that vanishes in
Cuba and Chile — so a backup scheduled at one of them runs at a time nobody
asked for, once a year. mcpd moves it forward to the next hour that does exist
rather than skipping it, and says so in the log. The default is 04:00.

Switching the schedule on does not take a backup straight away — the first one
is at the next time you named. If mcpd was down when a backup was due, it takes
**one** when it comes back, not one per week missed. And a run that is still
going makes the next one wait rather than starting beside it.

The passphrase is stored, because a backup that happens when nobody is present
needs one. Write it down and keep it with the backups: it is not shown again,
and a host that has lost its database cannot tell you what it was.

### What happened last night

**History** lists every run: when, how big, and what happened at each
destination. A run is *ok* when every destination took it, *partial* when some
did and some did not — there is a backup, it is just not everywhere — and
*failed* when none did.

*Interrupted* means mcpd stopped while the run was going. Some destinations may
hold that backup and some may not; it is deliberately not called a failure,
because it is not one thing or the other.

If notifications are configured, a failed or partial run sends one. A run that
worked sends nothing — a message every night is one people filter into a folder
they stop opening.

### If none of this fits

Point a **folder on this machine** at a directory, and point rclone, restic or
rsync at the same directory. mcpd writes a finished, encrypted file and nothing
else has to understand it.

### What the files are called

```
mcpd-nas-example-com-20260208T040000Z-a1b2c3d4.mcpdbak
     └── this host ──┘ └─ when, UTC ─┘ └ the run ┘
```

Only the timestamp is required; a host with no address configured has no middle
part. mcpd considers a file only if the name fits this shape **and** the host
part is its own, so two mcpds writing into one folder never touch each other's
backups, and a file anybody else put there is invisible.

The run identifier on the end is what keeps two backups taken in the same second
— a schedule firing as somebody presses the button — from being one file.

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

Every route here needs `admin`.

| | |
|---|---|
| `GET /api/backup` | What an archive taken now would hold, any staged restore, and the schedule: whether it is on, when it next runs, why it will not, and how the last one went. |
| `POST /api/backup` | `{"passphrase": "..."}` → the archive as a download. |
| `POST /api/backup/restore` | Multipart: `passphrase`, then `archive`. Checks it and restarts. |
| `DELETE /api/backup/restore` | Discards a staged restore. |
| `GET /api/backup/destinations` | Where backups go, and what this build can talk to. No credential is ever in the answer — only whether one is set. |
| `POST /api/backup/destinations` | Adds one. |
| `GET`, `PATCH`, `DELETE` `/api/backup/destinations/{id}` | One destination. A `PATCH` that does not mention the credential keeps the one there is; `""` clears it. The kind cannot be changed. |
| `POST /api/backup/destinations/{id}/test` | Reaches it, lists it, writes and removes a test file. Always `200`; the answer says whether it worked. For SFTP with nothing pinned, this records the key the server presented and returns it. |
| `POST /api/backup/run` | Starts a backup to every enabled destination. `202` with the run, or `409` when one is already going. |
| `GET /api/backup/runs?limit=` | The history, newest first. |
| `GET /api/backup/runs/{id}` | One run. |

The schedule and the passphrase are settings, so they are read and written
through `/api/settings` like everything else — `backup.schedule.enabled`,
`.cadence`, `.weekday`, `.time`, `.timezone`, and `backup.passphrase`.

The passphrase part of a restore must come before the file: the upload is
streamed straight into the decryption rather than buffered anywhere.

`POST /api/backup/restore` answers `202` with a `status` of either `restoring`
— the host is going down to apply it — or `staged`, meaning it was checked but
the restart could not be asked for and it will apply on the next start. The two
are worth telling apart: only the first is followed by a reconnect.
