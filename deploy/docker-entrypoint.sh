#!/bin/sh
#
# Generate a working deployment on first start, then hand over to mcpd.
#
# This is what the shell in the image is for. Distroless could run nothing
# before the binary, so a config file had to be hand-authored and bind-mounted
# and the data directory had to be pre-chowned to a uid the host user could
# not then read. Both of those were ergonomics problems dressed up as security
# ones: what actually hardens this container is the read-only root filesystem,
# every capability dropped, no-new-privileges, a nonroot user and a static
# CGO-free binary, and all five survive having /bin/sh here.
set -eu

# Arguments mean the caller knows what they want, so they get it unchanged.
# Every flag mcpd takes -- -version, -check, -backup, -init, -config -- is
# either an explicit choice of config or a thing that does its work and exits,
# and none of them asked for a deployment to be generated as a side effect.
# `docker compose run --rm mcpd sh` lands here too.
if [ "$#" -gt 0 ]; then
	case "$1" in
	-*) exec mcpd "$@" ;;
	*) exec "$@" ;;
	esac
fi

DATA_DIR="${MCPD_DATA_DIR:-/var/lib/mcpd}"
CONFIG="$DATA_DIR/config.yaml"
ENV_FILE="$DATA_DIR/.env"

if [ ! -d "$DATA_DIR" ] || [ ! -w "$DATA_DIR" ]; then
	echo "mcpd: $DATA_DIR is not writable by uid $(id -u):$(id -g)." >&2
	echo "mcpd: the host directory bind-mounted here must be owned by the uid" >&2
	echo "mcpd: the container runs as. Either chown it, or set UID and GID in" >&2
	echo "mcpd: .env to your own:  printf 'UID=%s\\nGID=%s\\n' \"\$(id -u)\" \"\$(id -g)\" >> .env" >&2
	exit 1
fi

if [ ! -e "$CONFIG" ]; then
	# Refuse rather than generate. `mcpd -init` would decline to overwrite the
	# .env anyway, and it should: that file holds the key every stored
	# credential was encrypted with, and a config generated beside secrets it
	# did not write is a deployment nobody can reason about.
	if [ -e "$ENV_FILE" ]; then
		echo "mcpd: $ENV_FILE exists but $CONFIG does not." >&2
		echo "mcpd: refusing to generate a config beside secrets it did not write." >&2
		echo "mcpd: restore config.yaml, or move the .env aside and start over." >&2
		exit 1
	fi

	echo "mcpd: no configuration in $DATA_DIR; generating one."
	# The address to advertise is a setting in the database now, not a key in
	# the generated file, so this seeds it -- exported only inside this branch,
	# which runs exactly once, on the start that has no configuration and no
	# database. Setting it on every start would mean an operator who corrects
	# the address on the Settings page is overruled by a container default they
	# cannot edit; mcpd would say the two disagree, but saying so every start
	# is not a fix.
	#
	# The listen addresses are the opposite case and stay overridden on every
	# start: what the process binds inside the container is decided by the port
	# mapping, not by anyone's editor.
	MCPD_PUBLIC_URL="${MCPD_PUBLIC_URL:-http://localhost:${MCPD_PORT:-8080}}"
	export MCPD_PUBLIC_URL

	mcpd -init "$DATA_DIR"

	# mcpd -init can only report the address it binds, which inside a container
	# is not the address anyone types. The port mapping is out here, so correct
	# it rather than leaving two answers standing.
	echo "mcpd: that dashboard address is the container's own port. From the" \
		"host it is http://localhost:${MCPD_FRONTEND_PORT:-80}"
fi

exec mcpd -config "$CONFIG"
