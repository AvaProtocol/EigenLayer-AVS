#!/usr/bin/env bash

set -xe 

forge script \
  script/DeployPauserRegistry.s.sol:DeployPauserRegistry \
  --rpc-url $RPC_URL \
  --private-key $PRIVATE_KEY \
  --etherscan-api-key $ETHSCAN_API_KEY \
  --broadcast --verify \
  -vvvv
  

