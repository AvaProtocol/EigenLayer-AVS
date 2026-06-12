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

# Auto-detect which binary this image carries:
#   - dockerfiles/operator.Dockerfile and dockerfiles/aggregator.Dockerfile
#     produce /ava (and this is what avaprotocol/ap-avs ships).
#   - The root Dockerfile produces ./ap (at WORKDIR /app) and is what
#     `make build` and any source-build Railway services use.
# Allow an explicit override via $AVS_BIN for unusual deployments.
if [ -n "$AVS_BIN" ] && [ -x "$AVS_BIN" ]; then
    BIN="$AVS_BIN"
elif [ -x /ava ]; then
    BIN=/ava
elif [ -x ./ap ]; then
    BIN=./ap
else
    echo "operator-entrypoint: no AVS binary found at \$AVS_BIN, /ava, or ./ap" >&2
    exit 1
fi

exec "$BIN" "$@"
