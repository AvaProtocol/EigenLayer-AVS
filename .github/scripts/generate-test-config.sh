#!/bin/bash
set -e

# Generate config files for CI unit tests by copying example and substituting secrets
# This script expects all environment variables to be set by the caller

echo "Generating test config files from example template..."

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
cp config/aggregator.example.yaml config/aggregator-sepolia.yaml

# Substitute secret values using unified environment variable names
sed -i "s|eth_rpc_url:.*|eth_rpc_url: ${CHAIN_RPC}|g" config/aggregator-sepolia.yaml
sed -i "s|eth_ws_url:.*|eth_ws_url: ${CHAIN_WS}|g" config/aggregator-sepolia.yaml
sed -i "s|ecdsa_private_key:.*|ecdsa_private_key: ${CONTROLLER_PRIVATE_KEY}|g" config/aggregator-sepolia.yaml
sed -i "s|bundler_url:.*|bundler_url: ${BUNDLER_RPC}|g" config/aggregator-sepolia.yaml
sed -i "s|controller_private_key:.*|controller_private_key: ${CONTROLLER_PRIVATE_KEY}|g" config/aggregator-sepolia.yaml
sed -i "s|paymaster_address:.*|paymaster_address: 0xd856f532F7C032e6b30d76F19187F25A068D6d92|g" config/aggregator-sepolia.yaml
sed -i "s|tenderly_account:.*|tenderly_account: ${TENDERLY_ACCOUNT}|g" config/aggregator-sepolia.yaml
sed -i "s|tenderly_project:.*|tenderly_project: ${TENDERLY_PROJECT}|g" config/aggregator-sepolia.yaml
sed -i "s|tenderly_access_key:.*|tenderly_access_key: ${TENDERLY_ACCESS_KEY}|g" config/aggregator-sepolia.yaml
sed -i "s|moralis_api_key:.*|moralis_api_key: ${MORALIS_API_KEY:-}|g" config/aggregator-sepolia.yaml

echo "Verifying config/aggregator-sepolia.yaml..."
echo "pwd=$(pwd)"
ls -la config/ || true
echo "Generated config/aggregator-sepolia.yaml (redacted first 40 lines):"
if [ -f config/aggregator-sepolia.yaml ]; then
  sed -n '1,40p' config/aggregator-sepolia.yaml | sed -e 's/tenderly_access_key:.*/tenderly_access_key: ***REDACTED***/' -e 's/bundler_url:.*/bundler_url: ***REDACTED***/' -e 's/ecdsa_private_key:.*/ecdsa_private_key: ***REDACTED***/' -e 's/controller_private_key:.*/controller_private_key: ***REDACTED***/'
fi
test -s config/aggregator-sepolia.yaml || (echo "config/aggregator-sepolia.yaml missing"; exit 1)

echo "✅ config/aggregator-sepolia.yaml generated successfully"
