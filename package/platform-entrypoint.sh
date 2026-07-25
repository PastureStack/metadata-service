#!/bin/bash
set -euo pipefail

ssl_cert_file=$(/usr/bin/update-platform-ca)
if [ -n "${ssl_cert_file}" ]; then
    export SSL_CERT_FILE="${ssl_cert_file}"
fi

exec "$@"
