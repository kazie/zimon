#!/bin/sh
# Container entrypoint: a fresh /data volume gets the image's default config,
# then zimon runs with the container's arguments (by default: serve).
set -e
if [ ! -w /data ]; then
    echo "zimon: /data is not writable by uid $(id -u). Use a named volume, or give a host" >&2
    echo "directory to that uid (Podman: UserNS=keep-id:uid=1001,gid=1001)." >&2
    exit 1
fi
if [ ! -e "$ZIMON_CONFIG" ]; then
    cp /etc/zimon/config.toml "$ZIMON_CONFIG"
fi
exec zimon "$@"
