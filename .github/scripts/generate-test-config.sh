#!/bin/bash
set -e

# Generate gateway-dev.yaml for CI unit tests by copying the .example
# template and substituting secrets from the caller's env. This is the
# test fixture that testutil/utils.go loads (DefaultConfigPath).
#
# The aggregator-sepolia.yaml shape that lived here pre-config-cleanup
# was the legacy single-chain template; gateway-dev.example.yaml is its
# multi-chain successor and shares the top-level fields the tests read.

echo "Generating test config files from gateway-dev.example.yaml..."

mkdir -p config

# Strip any leading scheme (https://, http://, wss://, ws://) so we can
# safely prepend the right one. The Test-environment secret may store
# either a hostname-only form or a full URL depending on how it was set.
CHAIN_HOST="${CHAIN_ENDPOINT#https://}"
CHAIN_HOST="${CHAIN_HOST#http://}"
CHAIN_HOST="${CHAIN_HOST#wss://}"
CHAIN_HOST="${CHAIN_HOST#ws://}"
CHAIN_RPC="https://${CHAIN_HOST}"
CHAIN_WS="wss://${CHAIN_HOST}"

# Copy example file as base
cp config/gateway-dev.example.yaml config/gateway-dev.yaml

# Substitute secret values using unified environment variable names.
#
# Heads up: these substitutions are unanchored, so they rewrite the
# field in EVERY YAML block — top-level AND each per-chain entry under
# `chains:`. That means the chain_id 84532 (base-sepolia) block ends
# up pointing at the same Sepolia RPC + bundler as the top-level
# block. This is intentional for the test fixture: every Go test that
# loads this file via testutil exercises Sepolia, none of them iterate
# `chains:`, and a uniformly-Sepolia file is preferable to a
# half-substituted one that leaves `${BASE_SEPOLIA_BUNDLER_URL}`-style
# env placeholders unresolved (the Go config loader would parse them
# as opaque strings, which can surface as confusing failures
# downstream). When a future test does exercise base-sepolia, switch
# to anchored sed or a yq-based rewrite then.
sed -i "s|eth_rpc_url:.*|eth_rpc_url: ${CHAIN_RPC}|g" config/gateway-dev.yaml
sed -i "s|eth_ws_url:.*|eth_ws_url: ${CHAIN_WS}|g" config/gateway-dev.yaml
sed -i "s|ecdsa_private_key:.*|ecdsa_private_key: ${CONTROLLER_PRIVATE_KEY}|g" config/gateway-dev.yaml
sed -i "s|bundler_url:.*|bundler_url: ${BUNDLER_RPC}|g" config/gateway-dev.yaml
sed -i "s|controller_private_key:.*|controller_private_key: ${CONTROLLER_PRIVATE_KEY}|g" config/gateway-dev.yaml
sed -i "s|paymaster_address:.*|paymaster_address: 0xd856f532F7C032e6b30d76F19187F25A068D6d92|g" config/gateway-dev.yaml
sed -i "s|tenderly_account:.*|tenderly_account: ${TENDERLY_ACCOUNT}|g" config/gateway-dev.yaml
sed -i "s|tenderly_project:.*|tenderly_project: ${TENDERLY_PROJECT}|g" config/gateway-dev.yaml
sed -i "s|tenderly_access_key:.*|tenderly_access_key: ${TENDERLY_ACCESS_KEY}|g" config/gateway-dev.yaml
sed -i "s|moralis_api_key:.*|moralis_api_key: ${MORALIS_API_KEY:-}|g" config/gateway-dev.yaml

echo "Verifying config/gateway-dev.yaml..."
echo "pwd=$(pwd)"
ls -la config/ || true
echo "Generated config/gateway-dev.yaml (redacted first 40 lines):"
if [ -f config/gateway-dev.yaml ]; then
  sed -n '1,40p' config/gateway-dev.yaml | sed -e 's/tenderly_access_key:.*/tenderly_access_key: ***REDACTED***/' -e 's/bundler_url:.*/bundler_url: ***REDACTED***/' -e 's/ecdsa_private_key:.*/ecdsa_private_key: ***REDACTED***/' -e 's/controller_private_key:.*/controller_private_key: ***REDACTED***/'
fi
test -s config/gateway-dev.yaml || (echo "config/gateway-dev.yaml missing"; exit 1)

echo "✅ config/gateway-dev.yaml generated successfully"
