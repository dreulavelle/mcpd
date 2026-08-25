#!/bin/sh
# Stop before the binary goes, so systemd is not left supervising a path that
# no longer exists.
set -e

case "$1" in
remove | deconfigure)
	if [ -d /run/systemd/system ]; then
		systemctl stop mcpd.service >/dev/null 2>&1 || true
		systemctl disable mcpd.service >/dev/null 2>&1 || true
	fi
	;;
esac

exit 0
