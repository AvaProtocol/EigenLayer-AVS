pragma solidity ^0.8.12;

import "forge-std/Script.sol";
import "forge-std/console2.sol";
import "@eigenlayer-core/test/mocks/EmptyContract.sol";

import "@openzeppelin/contracts/proxy/transparent/TransparentUpgradeableProxy.sol";
import "@openzeppelin/contracts/proxy/transparent/ProxyAdmin.sol";


 import "@eigenlayer-core/contracts/strategies/StrategyBaseTVLLimits.sol";
 import "@eigenlayer-core/contracts/core/AVSDirectory.sol";
 import "@eigenlayer-core/contracts/core/DelegationManager.sol";
 import "@eigenlayer-core/contracts/core/StrategyManager.sol";
 import "@eigenlayer-core/contracts/strategies/StrategyBaseTVLLimits.sol";
 import {IAVSDirectory} from "@eigenlayer-core/contracts/interfaces/IAVSDirectory.sol";
 import {PauserRegistry} from "@eigenlayer-core/contracts/permissions/PauserRegistry.sol";
 import {IRegistryCoordinator} from "@eigenlayer-middleware/interfaces/IRegistryCoordinator.sol";
 import {IStakeRegistry, IDelegationManager} from "@eigenlayer-middleware/interfaces/IStakeRegistry.sol";
 import {IIndexRegistry} from "@eigenlayer-middleware/interfaces/IIndexRegistry.sol";
 import {IBLSApkRegistry} from "@eigenlayer-middleware/interfaces/IBLSApkRegistry.sol";
 import {RegistryCoordinator} from "@eigenlayer-middleware/RegistryCoordinator.sol";
 import {IndexRegistry} from "@eigenlayer-middleware/IndexRegistry.sol";
 import {StakeRegistry, IStrategy} from "@eigenlayer-middleware/StakeRegistry.sol";
 import {BLSApkRegistry} from "@eigenlayer-middleware/BLSApkRegistry.sol";
 import {OperatorStateRetriever} from "@eigenlayer-middleware/OperatorStateRetriever.sol";

import {AutomationServiceManager} from "../src/core/AutomationServiceManager.sol";
import {IAutomationServiceManager} from "../src/interfaces/IAutomationServiceManager.sol";

contract DeployServiceManager is Script {
      struct AutomationServiceContract {
          AutomationServiceManager automationServiceManager;
          AutomationServiceManager automationServiceManagerImplementation;
          RegistryCoordinator registryCoordinator;
          IRegistryCoordinator registryCoordinatorImplementation;
          IIndexRegistry indexRegistry;
          IIndexRegistry indexRegistryImplementation;
          IStakeRegistry stakeRegistry;
          IStakeRegistry stakeRegistryImplementation;
          BLSApkRegistry apkRegistry;
          BLSApkRegistry apkRegistryImplementation;
          OperatorStateRetriever operatorStateRetriever;
      }

    struct TokenAndWeight {
        address token;
        uint96 weight;
    }

    struct DeployParam {
		uint256 numQuorum;
        uint256 maxOperatorCount;
        uint96 minimumStake;
        uint256 numStrategies;

        address pauserRegistry;
        address ownerAddress;
        address whitelister;

        address delegationManager;
        address avsDirectory;

        address beaconETH;
        address stETH;
        address wETH;
    }



    function run() external {
		string memory defaultRegistryPath = "./script/output/deploy_output.json";
  		string memory deployedRegistryPath = vm.envOr("REGISTRY_PATH", defaultRegistryPath);

        uint256 deployerPrivateKey = vm.envUint("PRIVATE_KEY");

        DeployParam memory dp = DeployParam({
		    numQuorum: 1,
            maxOperatorCount: 50,
            minimumStake: 0,
            numStrategies: 3,

        pauserRegistry: vm.envAddress("PAUSER_REGISTRY_ADDRESS"),
        ownerAddress: vm.envAddress("OWNER_ADDRESS"),
        whitelister: vm.envAddress("OWNER_ADDRESS"),

        delegationManager: vm.envAddress("DELEGATION_MANAGER"),
        avsDirectory: vm.envAddress("AVS_DIRECTORY"),

        beaconETH: vm.envAddress("STRATEGY_B_ETH"),
        stETH: vm.envAddress("STRATEGY_ST_ETH"),
        wETH: vm.envAddress("STRATEGY_WETH")
        });

        TokenAndWeight[] memory deployedStrategyArray = new TokenAndWeight[](dp.numStrategies);

        {
            // need manually step in
            deployedStrategyArray[0].token = dp.beaconETH;
            deployedStrategyArray[1].token = dp.stETH;
            deployedStrategyArray[2].token = dp.wETH;
        }

        {
            deployedStrategyArray[0].weight = 1000000000000000000;
            deployedStrategyArray[1].weight = 997992210000000000;
            deployedStrategyArray[2].weight = 997992210000000000;
        }


        vm.startBroadcast(deployerPrivateKey);

        ProxyAdmin oakAVSProxyAdmin = new ProxyAdmin();
        EmptyContract emptyContract = new EmptyContract();

        //AutomationServiceManager asm = new AutomationServiceManager(pauserRegistry, false, ownerAddress, whitelister);

        AutomationServiceContract memory automationServiceContract;

        automationServiceContract.indexRegistry = IIndexRegistry(
            address(new TransparentUpgradeableProxy(address(emptyContract), address(oakAVSProxyAdmin), ""))
        );
        automationServiceContract.stakeRegistry = IStakeRegistry(
            address(new TransparentUpgradeableProxy(address(emptyContract), address(oakAVSProxyAdmin), ""))
        );
        automationServiceContract.apkRegistry = BLSApkRegistry(
            address(new TransparentUpgradeableProxy(address(emptyContract), address(oakAVSProxyAdmin), ""))
        );
        automationServiceContract.registryCoordinator = RegistryCoordinator(
            address(new TransparentUpgradeableProxy(address(emptyContract), address(oakAVSProxyAdmin), ""))
        );
        automationServiceContract.automationServiceManager = AutomationServiceManager(
            address(new TransparentUpgradeableProxy(address(emptyContract), address(oakAVSProxyAdmin), ""))
        );


        // Now do the real dep
        // Second, deploy the *implementation* contracts, using the *proxy contracts* as inputs
        automationServiceContract.indexRegistryImplementation = new IndexRegistry(automationServiceContract.registryCoordinator);
        oakAVSProxyAdmin.upgrade(
            TransparentUpgradeableProxy(payable(address(automationServiceContract.indexRegistry))),
            address(automationServiceContract.indexRegistryImplementation)
        );

        automationServiceContract.stakeRegistryImplementation = new StakeRegistry(
            automationServiceContract.registryCoordinator, IDelegationManager(dp.delegationManager)
        );
        oakAVSProxyAdmin.upgrade(
            TransparentUpgradeableProxy(payable(address(automationServiceContract.stakeRegistry))),
            address(automationServiceContract.stakeRegistryImplementation)
        );

        automationServiceContract.apkRegistryImplementation = new BLSApkRegistry(automationServiceContract.registryCoordinator);
        oakAVSProxyAdmin.upgrade(
            TransparentUpgradeableProxy(payable(address(automationServiceContract.apkRegistry))),
            address(automationServiceContract.apkRegistryImplementation)
        );

        automationServiceContract.registryCoordinatorImplementation = new RegistryCoordinator(
            IAutomationServiceManager(address(automationServiceContract.automationServiceManager)),
            automationServiceContract.stakeRegistry,
            automationServiceContract.apkRegistry,
            automationServiceContract.indexRegistry
        );
        automationServiceContract.operatorStateRetriever = new OperatorStateRetriever();




        {
            IRegistryCoordinator.OperatorSetParam[] memory operatorSetParams =
                new IRegistryCoordinator.OperatorSetParam[](dp.numQuorum);

            // prepare _operatorSetParams
            for (uint256 i = 0; i < dp.numQuorum; i++) {
                // hard code these for now
                operatorSetParams[i] = IRegistryCoordinator.OperatorSetParam({
                    maxOperatorCount: uint32(dp.maxOperatorCount),
                    kickBIPsOfOperatorStake: 11000, // an operator needs to have kickBIPsOfOperatorStake / 10000 times the stake of the operator with the least stake to kick them out
                    kickBIPsOfTotalStake: 1001 // an operator needs to have less than kickBIPsOfTotalStake / 10000 of the total stake to be kicked out
                });
            }

            // prepare _minimumStakes
            uint96[] memory minimumStakeForQuourm = new uint96[](dp.numQuorum);
            for (uint256 i = 0; i < dp.numQuorum; i++) {
                minimumStakeForQuourm[i] = dp.minimumStake;
            }

            // prepare _strategyParams
            IStakeRegistry.StrategyParams[][] memory strategyParams =
                new IStakeRegistry.StrategyParams[][](dp.numQuorum);
            for (uint256 i = 0; i < dp.numQuorum; i++) {
                IStakeRegistry.StrategyParams[] memory params =
                    new IStakeRegistry.StrategyParams[](dp.numStrategies);
                for (uint256 j = 0; j < dp.numStrategies; j++) {
                    params[j] = IStakeRegistry.StrategyParams({
                        strategy: IStrategy(deployedStrategyArray[j].token),
                        multiplier: deployedStrategyArray[j].weight
                    });
                }
                strategyParams[i] = params;
            }

            // initialize
            oakAVSProxyAdmin.upgradeAndCall(
                TransparentUpgradeableProxy(payable(address(automationServiceContract.registryCoordinator))),
                address(automationServiceContract.registryCoordinatorImplementation),
                abi.encodeWithSelector(
                    RegistryCoordinator.initialize.selector,
                    dp.ownerAddress,
                    dp.ownerAddress,
                    dp.ownerAddress,
                    //IPauserRegistry(pauserRegistry),
                    dp.pauserRegistry,
                    0, // initial paused status is nothing paused
                    operatorSetParams,
                    minimumStakeForQuourm,
                    strategyParams
                )
            );
        }



        automationServiceContract.automationServiceManagerImplementation = new AutomationServiceManager(
            IAVSDirectory(dp.avsDirectory),
            automationServiceContract.registryCoordinator,
            automationServiceContract.stakeRegistry
        );
        // Third, upgrade the proxy contracts to use the correct implementation contracts and initialize them.
        oakAVSProxyAdmin.upgradeAndCall(
            TransparentUpgradeableProxy(payable(address(automationServiceContract.automationServiceManager))),
            address(automationServiceContract.automationServiceManagerImplementation),
            abi.encodeWithSelector(
                AutomationServiceManager.initialize.selector,
                //IPauserRegistry(pauserRegistry),
                dp.pauserRegistry,
                0,
                dp.ownerAddress,
                dp.whitelister
            )
        );


        vm.stopBroadcast();

        vm.createDir("./script/output", true);

        string memory output = "avs info deployment output";
        vm.serializeAddress(output, "proxyAdmin", address(oakAVSProxyAdmin));

        vm.serializeAddress(output, "avsServiceManagerProxy", address(automationServiceContract.automationServiceManager));
        vm.serializeAddress(output, "indexRegistryProxy", address(automationServiceContract.indexRegistry));
        vm.serializeAddress(output, "stakeRegistryProxy", address(automationServiceContract.stakeRegistry));

        string memory registryJson = vm.serializeString(output, "object", output);
        vm.writeJson(registryJson, deployedRegistryPath);
    }
}
