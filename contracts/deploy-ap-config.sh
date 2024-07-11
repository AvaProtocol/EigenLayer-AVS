#!/usr/bin/env bash

set -xe 

forge script \
  script/DeployAPConfig.s.sol:DeployAPConfig \
  --rpc-url $RPC_URL \
  --private-key $PRIVATE_KEY \
  --etherscan-api-key $ETHSCAN_API_KEY \
  --broadcast --verify \
  --slow \
  -vvvv
  

