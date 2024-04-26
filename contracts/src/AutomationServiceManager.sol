// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.9;

import "@eigenlayer/contracts/libraries/BytesLib.sol";
import "./IAutomationTaskManager.sol";
import "@eigenlayer-middleware/src/ServiceManagerBase.sol";

/**
 * @title Primary entrypoint for procuring services from Automation.
 * @author Layr Labs, Inc.
 */
contract AutomationServiceManager is ServiceManagerBase {
    using BytesLib for bytes;

    // Oak TaskManager contract
    IAutomationTaskManager
        public immutable oakAutomationTaskManager;

    /// @notice when applied to a function, ensures that the function is only callable by the `registryCoordinator`.
    modifier onlyAutomationTaskManager() {
        require(
            msg.sender == address(oakAutomationTaskManager),
            "onlyAutomationTaskManager: not from credible Oak automation task manager"
        );
        _;
    }

    constructor(
        IDelegationManager _delegationManager,
        IRegistryCoordinator _registryCoordinator,
        IStakeRegistry _stakeRegistry,
        IAutomationTaskManager _oakAutomationTaskManager
    )
        ServiceManagerBase(
            _delegationManager,
            _registryCoordinator,
            _stakeRegistry
        )
    {
        oakAutomationTaskManager = _oakAutomationTaskManager;
    }

    /// @notice Called in the event of challenge resolution, in order to forward a call to the Slasher, which 'freezes' the `operator`.
    /// @dev The Slasher contract is under active development and its interface expected to change.
    ///      We recommend writing slashing logic without integrating with the Slasher at this point in time.
    function freezeOperator(
        address operatorAddr
    ) external onlyAutomationTaskManager {
        // TODO: Impelemnt freeze and slash later
        // slasher.freezeOperator(operatorAddr);
    }
}
