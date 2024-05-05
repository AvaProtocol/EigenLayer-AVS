// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.12;

import "forge-std/Script.sol";
import {RegistryCoordinator} from "@eigenlayer-middleware/RegistryCoordinator.sol";

import {AutomationTaskManager} from "../src/core/AutomationTaskManager.sol";

contract DeployTaskManagerImpl is Script {
    function run() external {
        address registryCoordinator = vm.envAddress("REGISTRY_COORDINATOR_ADDRESS");

        vm.startBroadcast();

        AutomationTaskManager automationTaskManager = new AutomationTaskManager(
            RegistryCoordinator(registryCoordinator), 12
        );

        vm.stopBroadcast();

        string memory output = "task manager info deployment output";
        vm.serializeAddress(output, "automationTaskManagerImpl", address(automationTaskManager));

        string memory registryJson = vm.serializeString(output, "object", output);
        vm.writeJson(registryJson, "./script/output/deploy_task_manager_impl.json");

    }
}
