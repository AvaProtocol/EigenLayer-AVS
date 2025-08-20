#!/bin/bash
set -e

# Generate config/aggregator.yaml for CI unit tests
# This script expects all environment variables to be set by the caller

echo "Generating config/aggregator.yaml for tests..."

mkdir -p config
cat > config/aggregator.yaml <<YAML
# Auto-generated for CI unit tests
environment: development

# Global RPCs used by components that read top-level fields
eth_rpc_url: "${SEPOLIA_RPC}"
eth_ws_url: "${SEPOLIA_WS}"

# ECDSA private key for aggregator (from test config)
ecdsa_private_key: "${CONTROLLER_PRIVATE_KEY}"

smart_wallet:
  eth_rpc_url: "${SEPOLIA_RPC}"
  eth_ws_url: "${SEPOLIA_WS}"
  bundler_url: "${SEPOLIA_BUNDLER_RPC}"
  controller_private_key: "${CONTROLLER_PRIVATE_KEY}"
  # Omit factory_address, entrypoint_address, and paymaster_address to use defaults from config.go constants:
  # - DefaultFactoryProxyAddressHex = "0xB99BC2E399e06CddCF5E725c0ea341E8f0322834"
  # - DefaultEntrypointAddressHex = "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"  
  # - DefaultPaymasterAddressHex = "0xB985af5f96EF2722DC99aEBA573520903B86505e"

# Tenderly HTTP Simulation API
tenderly_account: "${TENDERLY_ACCOUNT}"
tenderly_project: "${TENDERLY_PROJECT}"
tenderly_access_key: "${TENDERLY_ACCESS_KEY}"

# Test private key (only used in tests)
test_private_key: "${CONTROLLER_PRIVATE_KEY}"
YAML

echo "Verifying config/aggregator.yaml..."
echo "pwd=$(pwd)"
ls -la config/ || true
echo "Generated config/aggregator.yaml (redacted first 40 lines):"
if [ -f config/aggregator.yaml ]; then
  sed -n '1,40p' config/aggregator.yaml | sed -e 's/tenderly_access_key:.*/tenderly_access_key: ***REDACTED***/' -e 's/test_private_key:.*/test_private_key: ***REDACTED***/' -e 's/bundler_url:.*/bundler_url: ***REDACTED***/' -e 's/ecdsa_private_key:.*/ecdsa_private_key: ***REDACTED***/'
fi
test -s config/aggregator.yaml || (echo "config/aggregator.yaml missing"; exit 1)

echo "✅ config/aggregator.yaml generated successfully"
