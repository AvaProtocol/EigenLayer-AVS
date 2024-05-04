pragma solidity ^0.8.25;

import "forge-std/Script.sol";
import "@eigenlayer-core/contracts/permissions/PauserRegistry.sol";

contract DeployPauserRegistry is Script {
    function run() external {
		string memory defaultRegistryPath = "./script/output/registry_deploy_output.json";
  		string memory deployedRegistryPath = vm.envOr("REGISTRY_PATH", defaultRegistryPath);

        uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");
        address pauserAddress = vm.envAddress("PAUSER_ADDRESS");

        address[] memory pausers = new address[](1);
        pausers[0] = pauserAddress;

        vm.startBroadcast(deployerPrivateKey);

        PauserRegistry r = new PauserRegistry(pausers, pauserAddress);

        vm.stopBroadcast();

        vm.createDir("./script/output", true);

        string memory output = "registry info deployment output";
        vm.serializeAddress(output, "pauserRegistryAddress", address(r));

        string memory registryJson = vm.serializeString(output, "object", output);
        vm.writeJson(registryJson, deployedRegistryPath);
    }
}
