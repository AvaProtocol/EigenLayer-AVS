#!/bin/bash
set -e

# Generate config files for CI unit tests by copying example and substituting secrets
# This script expects all environment variables to be set by the caller

echo "Generating test config files from example template..."

mkdir -p config

# Copy example file as base
cp config/aggregator.example.yaml config/aggregator-sepolia.yaml

# Substitute secret values for Sepolia
sed -i "s|eth_rpc_url:.*|eth_rpc_url: ${SEPOLIA_RPC}|g" config/aggregator-sepolia.yaml
sed -i "s|eth_ws_url:.*|eth_ws_url: ${SEPOLIA_WS}|g" config/aggregator-sepolia.yaml
sed -i "s|ecdsa_private_key:.*|ecdsa_private_key: ${CONTROLLER_PRIVATE_KEY}|g" config/aggregator-sepolia.yaml
sed -i "s|bundler_url:.*|bundler_url: ${SEPOLIA_BUNDLER_RPC}|g" config/aggregator-sepolia.yaml
sed -i "s|controller_private_key:.*|controller_private_key: ${CONTROLLER_PRIVATE_KEY}|g" config/aggregator-sepolia.yaml
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
