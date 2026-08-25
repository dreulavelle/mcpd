# Upgrading

## Keeping current

The **System** page reports the running version and every release published
since it, with the notes from each. Turn the check on under Settings ->
Updates; it is off by default because reaching github.com on a timer is a
connection worth agreeing to rather than discovering.

mcpd never installs an update itself. Replacing a running host means replacing
a binary or an image, which needs privileges the deployment drops on purpose:
the container runs read-only, as a non-root user, with every capability
dropped. Handing it the Docker socket to make a one-click update work would
undo all three at once, on a host holding your credentials.

```bash
docker compose pull && docker compose up -d     # Docker
sudo apt install ./mcpd_<version>_amd64.deb     # Debian package
sudo systemctl restart mcpd
```

Migrations are forward-only and run on the next start, so an upgrade needs no
other step. Changing a plugin's credentials needs no restart at all: the plugin
is remounted and its tunnel rebuilt, so a connector picks up the new credential
on its next call.

## Moving a deployment from before ./data


Before this, the container kept its data in `./.data`, its config in
`./config.yaml`, and its secrets in `./.env`. All three move into `./data`, and
nothing is regenerated — the point of doing it by hand is that the existing
`MCPD_SECRET_KEY` comes with you.

```bash
docker compose down

# .data was owned by uid 65532, which is the thing this release fixes. This is
# the last time you need sudo for it.
sudo chown -R "$(id -u):$(id -g)" .data

mkdir -p data/plugins
cp -a .data/.   data/               # database, TLS material, plugin binaries
cp -a config.yaml data/config.yaml  # storage paths inside it are container
                                    # paths and do not change
cp -a .env      data/.env           # keeps the existing MCPD_SECRET_KEY

# The root .env is now only the published ports, which is what compose reads.
printf 'MCPD_PORT=8080\nMCPD_BIND=127.0.0.1\nMCPD_FRONTEND_PORT=80\nMCPD_FRONTEND_BIND=127.0.0.1\n' > .env
printf 'UID=%s\nGID=%s\n' "$(id -u)" "$(id -g)" >> .env

docker compose up -d --build
```

Nothing is deleted, so the old layout is still there if this goes wrong. Check
`docker compose logs` for `database ready`, then open a plugin's settings: if a
credential you saved is still there, the key came across. Once it has,
`rm -rf .data config.yaml` — and note that `mv .data data` is not the move to
make, because `./data` already exists and you would end up with `data/.data`.
