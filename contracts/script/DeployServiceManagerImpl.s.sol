// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.12;

import "forge-std/Script.sol";
import {AutomationServiceManager} from "../src/core/AutomationServiceManager.sol";
import {IStakeRegistry} from "@eigenlayer-middleware/interfaces/IStakeRegistry.sol";
import {IRegistryCoordinator} from "@eigenlayer-middleware/interfaces/IRegistryCoordinator.sol";
import {IAVSDirectory} from "@eigenlayer-core/contracts/interfaces/IAVSDirectory.sol";

contract DeployServiceManagerImpl is Script {
    function run() external {
        address avsDirectory = vm.envAddress("AVS_DIRECTORY");

        // RegistryCoordinator and StakeRegistry is deployed separately
        address registryCoordinator = vm.envAddress("REGISTRY_COORDINATOR_ADDRESS");
        address stakeRegistry = vm.envAddress("STAKE_REGISTRY_ADDRESS");

        vm.startBroadcast();

        AutomationServiceManager automationServiceManagerImplementation = new AutomationServiceManager(
            IAVSDirectory(avsDirectory), IRegistryCoordinator(registryCoordinator), IStakeRegistry(stakeRegistry)
        );

        vm.stopBroadcast();
    }
}
