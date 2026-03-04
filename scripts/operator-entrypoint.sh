#!/bin/sh
# Entrypoint for operator on Railway
# Writes key files from environment variables, then starts the operator.
#
# Required env vars:
#   ECDSA_KEY_JSON - contents of the ECDSA keystore JSON file
#   BLS_KEY_JSON   - contents of the BLS keystore JSON file
#   OPERATOR_KEY_PASSWORD - password for both keystores

set -e

mkdir -p /app/keys

if [ -n "$ECDSA_KEY_JSON" ]; then
    echo "$ECDSA_KEY_JSON" > /app/keys/ecdsa.key.json
    echo "Wrote ECDSA key file"
fi

if [ -n "$BLS_KEY_JSON" ]; then
    echo "$BLS_KEY_JSON" > /app/keys/bls.key.json
    echo "Wrote BLS key file"
fi

exec ./ap "$@"
