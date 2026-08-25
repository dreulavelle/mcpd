#!/bin/sh
# purge deliberately keeps /var/lib/mcpd. It holds the key every stored
# credential is encrypted with, and that is not something a tidy-up should
# destroy without being asked.
set -e

STATE=/var/lib/mcpd

case "$1" in
remove)
	if [ -d /run/systemd/system ]; then
		systemctl daemon-reload || true
	fi
	;;
purge)
	if [ -d /run/systemd/system ]; then
		systemctl daemon-reload || true
	fi
	if [ -d "$STATE" ]; then
		echo "mcpd: $STATE was left in place. It holds the database and the key"
		echo "mcpd: every stored credential is encrypted with, which cannot be"
		echo "mcpd: recovered once deleted. Remove it yourself if you mean to:"
		echo "mcpd:   sudo rm -rf $STATE"
	fi
	if getent passwd mcpd >/dev/null; then
		deluser --system mcpd >/dev/null 2>&1 || userdel mcpd >/dev/null 2>&1 || true
	fi
	;;
esac

exit 0
