// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.12;

import "forge-std/Script.sol";

import "@openzeppelin/contracts/proxy/transparent/TransparentUpgradeableProxy.sol";
import "@openzeppelin/contracts/proxy/transparent/ProxyAdmin.sol";

import {APConfig} from "../src/core/APConfig.sol";

// To deployment and swap the implementation set 2 envs:
// CREATE_PROXY=false
// AP_PROXY_ADDRESS=0x123 
// PROXY_ADMIN_ADDRESS=0x456
// When seeing the two env, the script will upgrade the underlying contract
//
// Example: 
//   AP_PROXY_ADDRESS=0xb8abbb082ecaae8d1cd68378cf3b060f6f0e07eb \
//     SWAP_IMPL=true bash deploy-ap-config.sh
contract DeployAPConfig is Script {
    function run() external {
        address oakAVSProxyAdmin = vm.envAddress("PROXY_ADMIN_ADDRESS");
        bool swapImpl =  vm.envBool("SWAP_IMPL");

        vm.startBroadcast();

        string memory output = "APConfig deployment output";

        // 1. Deploy the implementation
        APConfig apConfig = new APConfig();
        vm.serializeAddress(output, "apConfigImpl", address(apConfig));

        // 2. Swap impl or deploy new proxy and bind to
        if (swapImpl) {
            ProxyAdmin oakProxyAdmin =
                ProxyAdmin(oakAVSProxyAdmin);

            // Load existing proxy and upgrade that to this new impl
            address apProxyAddress =  vm.envAddress("AP_PROXY_ADDRESS");
            oakProxyAdmin.upgrade(
                TransparentUpgradeableProxy(payable(apProxyAddress)),
                address(apConfig)
            );
        } else {
            // Deploy the proxy contract, bind it to the impl
            TransparentUpgradeableProxy proxy = new TransparentUpgradeableProxy(
                address(apConfig),
                address(oakAVSProxyAdmin),
                ""
            );
            vm.serializeAddress(output, "apConfigProxy", address(proxy));
        }

        string memory registryJson = vm.serializeString(output, "object", output);
        vm.writeJson(registryJson, "./script/output/ap_config.json");

        vm.stopBroadcast();
    }
}
