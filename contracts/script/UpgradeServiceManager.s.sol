// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.12;

import "forge-std/Script.sol";
import "@openzeppelin/contracts/proxy/transparent/ProxyAdmin.sol";
import {AutomationServiceManager} from "../src/core/AutomationServiceManager.sol";

contract UpgradeServiceManager is Script {
    function run() external {
        address proxyAdmin = vm.envAddress("PROXY_ADMIN_ADDRESS");

        address automationServiceManager = vm.envAddress("SERVICE_MANAGER_PROXY_ADDRESS");

        address newServiceManagerAddress = vm.envAddress("NEW_SERVICE_MANAGER_ADDRESS");

        ProxyAdmin oakProxyAdmin = ProxyAdmin(proxyAdmin);

        vm.startBroadcast();

        oakProxyAdmin.upgrade(
            TransparentUpgradeableProxy(payable(address(automationServiceManager))), address(newServiceManagerAddress)
        );
        vm.stopBroadcast();
    }
}
