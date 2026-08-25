#!/bin/sh
# Makes the service account, the state directory and a configuration -- what
# the container entrypoint does on first start.
set -e

USER=mcpd
GROUP=mcpd
STATE=/var/lib/mcpd

case "$1" in
configure)
	# Idempotent: this runs again on every upgrade. useradd is a fallback for
	# images without adduser, where dying here would leave dpkg half-configured.
	if ! getent group "$GROUP" >/dev/null; then
		if command -v addgroup >/dev/null 2>&1; then
			addgroup --system "$GROUP"
		else
			groupadd --system "$GROUP"
		fi
	fi
	if ! getent passwd "$USER" >/dev/null; then
		if command -v adduser >/dev/null 2>&1; then
			adduser --system --ingroup "$GROUP" --home "$STATE" \
				--no-create-home --shell /usr/sbin/nologin \
				--gecos "mcpd service account" "$USER"
		else
			useradd --system --gid "$GROUP" --home-dir "$STATE" \
				--no-create-home --shell /usr/sbin/nologin \
				--comment "mcpd service account" "$USER"
		fi
	fi

	# 0750: the database and the key it is encrypted with live here.
	mkdir -p "$STATE"
	chown "$USER":"$GROUP" "$STATE"
	chmod 0750 "$STATE"

	# Only when there is not one: replacing .env on upgrade would make every
	# stored credential unreadable.
	if [ ! -e "$STATE/config.yaml" ]; then
		echo "mcpd: generating a configuration in $STATE"
		if ! runuser -u "$USER" -- /usr/bin/mcpd -init "$STATE" >/dev/null 2>&1 \
			&& ! su -s /bin/sh -c "/usr/bin/mcpd -init $STATE" "$USER" >/dev/null 2>&1; then
			echo "mcpd: could not generate a configuration; run" >&2
			echo "  sudo -u $USER mcpd -init $STATE" >&2
		fi
	fi

	if [ -d /run/systemd/system ]; then
		systemctl daemon-reload || true
		systemctl enable mcpd.service >/dev/null 2>&1 || true
		# Allowed to fail: port 80 may be taken, and that should not abort
		# the install.
		if ! systemctl restart mcpd.service; then
			echo "mcpd: installed, but the service did not start." >&2
			echo "mcpd: check 'systemctl status mcpd' and 'journalctl -u mcpd'." >&2
			echo "mcpd: the dashboard binds port 80 by default." >&2
		fi
	fi
	;;
esac

exit 0
