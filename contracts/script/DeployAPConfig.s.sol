// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.12;

import "forge-std/Script.sol";

import "@openzeppelin/contracts/proxy/transparent/TransparentUpgradeableProxy.sol";
import "@openzeppelin/contracts/proxy/transparent/ProxyAdmin.sol";

import {APConfig} from "../src/core/APConfig.sol";

contract DeployAPConfig is Script {
    function run() external {
        address oakAVSProxyAdmin =  vm.envAddress("PROXY_ADMIN_ADDRESS");

        vm.startBroadcast();

        // 1. Deploy the implementation
         APConfig apConfig = new APConfig();

        // 1. Deploy the proxy contract first if needed
        // When re-deploying we won't need to run this but get the address from
        TransparentUpgradeableProxy proxy = new TransparentUpgradeableProxy(
            address(apConfig),
            address(oakAVSProxyAdmin),
            ""
        );

        string memory output = "APConfig deployment output";
        vm.serializeAddress(output, "apConfigImpl", address(apConfig));
        vm.serializeAddress(output, "apConfigProxy", address(proxy));

        string memory registryJson = vm.serializeString(output, "object", output);
        vm.writeJson(registryJson, "./script/output/ap_config.json");

    }
}
