#!/bin/bash
set -euo pipefail

metadata_ip=169.254.169.250/32

if [ "$#" -eq 0 ]; then
    set -- metadata-service
elif [[ "$1" == -* ]]; then
    set -- metadata-service "$@"
fi

if [ "$(id -u)" = "0" ]; then
    if ! ip -4 addr show dev eth0 | grep -q '169[.]254[.]169[.]250/32'; then
        ip addr add "${metadata_ip}" dev eth0
    fi
    exec setpriv --reuid=10001 --regid=10001 --init-groups "$@"
fi

exec "$@"
