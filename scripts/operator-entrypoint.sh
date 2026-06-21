#!/bin/sh
# Shared runtime entrypoint for Railway services (gateway, workers,
# operators). It materializes per-deployment inputs that we deliberately
# keep OUT of the public avaprotocol/ap-avs image — delivered instead as
# environment variables — then execs the AVS binary with the given args.
#
# Optional env vars:
#   AP_CONFIG_YAML - full service config YAML. When set, written verbatim to
#                    /app/config/runtime.yaml so the service can run with
#                    `--config=config/runtime.yaml`. This lets the public image
#                    ship no deployment-specific config; the per-service YAML
#                    lives in avs-infra. Any ${VAR} placeholders inside are
#                    expanded later by core/config.ReadYamlConfig from the other
#                    env vars, so they are written here untouched.
#   ECDSA_KEY_JSON - contents of the ECDSA keystore JSON file (operators)
#   BLS_KEY_JSON   - contents of the BLS keystore JSON file (operators)
#   OPERATOR_KEY_PASSWORD - password for both keystores (operators)

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

# Runtime config injection (env-var-driven deployment). When AP_CONFIG_YAML is
# set, write it verbatim to /app/config/runtime.yaml so the start command can
# point at `--config=config/runtime.yaml`. printf '%s' avoids interpreting
# backslashes and leaves ${VAR} placeholders intact for ReadYamlConfig.
if [ -n "$AP_CONFIG_YAML" ]; then
    mkdir -p /app/config
    printf '%s' "$AP_CONFIG_YAML" > /app/config/runtime.yaml
    echo "Wrote runtime config to /app/config/runtime.yaml ($(wc -c < /app/config/runtime.yaml) bytes)"
fi

# Auto-detect which binary this image carries:
#   - dockerfiles/operator.Dockerfile and dockerfiles/aggregator.Dockerfile
#     produce /ava (and this is what avaprotocol/ap-avs ships).
#   - The root Dockerfile produces ./ap (at WORKDIR /app) and is what
#     `make build` and any source-build Railway services use.
# Allow an explicit override via $AVS_BIN for unusual deployments. When
# $AVS_BIN is set but unusable, fail loudly rather than fall through to
# the auto-detected default — silent fallthrough would be surprising to
# an operator who set the override on purpose and didn't notice the typo.
if [ -n "$AVS_BIN" ]; then
    if [ ! -x "$AVS_BIN" ]; then
        echo "operator-entrypoint: AVS_BIN=$AVS_BIN is set but not executable" >&2
        exit 1
    fi
    BIN="$AVS_BIN"
elif [ -x /ava ]; then
    BIN=/ava
elif [ -x ./ap ]; then
    BIN=./ap
else
    echo "operator-entrypoint: no AVS binary found at /ava or ./ap (set AVS_BIN to override)" >&2
    exit 1
fi

exec "$BIN" "$@"
