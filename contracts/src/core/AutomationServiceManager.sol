pragma solidity ^0.8.12;

import { EnumerableSet } from '@openzeppelin/contracts/utils/structs/EnumerableSet.sol';
import { Pausable } from '@eigenlayer-core/contracts/permissions/Pausable.sol';
import { IAVSDirectory } from '@eigenlayer-core/contracts/interfaces/IAVSDirectory.sol';
import { ISignatureUtils } from '@eigenlayer-core/contracts/interfaces/ISignatureUtils.sol';
import { IPauserRegistry } from '@eigenlayer-core/contracts/interfaces/IPauserRegistry.sol';
import { IServiceManager } from '@eigenlayer-middleware/interfaces/IServiceManager.sol';
import { IStakeRegistry } from '@eigenlayer-middleware/interfaces/IStakeRegistry.sol';
import { IRegistryCoordinator } from '@eigenlayer-middleware/interfaces/IRegistryCoordinator.sol';
import { BLSSignatureChecker } from '@eigenlayer-middleware/BLSSignatureChecker.sol';
import { ServiceManagerBase } from '@eigenlayer-middleware/ServiceManagerBase.sol';
import { AutomationServiceManagerStorage } from './AutomationServiceManagerStorage.sol';
import { IAutomationServiceManager } from '../interfaces/IAutomationServiceManager.sol';
import { AutomationTaskManager } from '../core/AutomationTaskManager.sol';
import { IAutomationTaskManager } from '../interfaces/IAutomationTaskManager.sol';

error ZeroAddress();
error InvalidStartIndex();
error InvalidConfirmer();
error NotWhitelister();
error InvalidSender();
error InvalidReferenceBlockNum();
error InsufficientThreshold();
error InsufficientThresholdPercentages();
error InvalidQuorumParam();
error InvalidQuorumThresholdPercentage();
error AlreadyInAllowlist();
error NotInAllowlist();
error AlreadyAdded();
error ResolvedAlert();
error AlreadyEnabled();
error AlreadyDisabled();

// Common
error AlreadyInitialized();
error NotInitialized();
error ZeroValue();

error UselessAlert();
error InvalidAlert();
error InvalidAlertType();
error InvalidProvedIndex();
error InvalidCheckpoint();
error InvalidIndex();

error ProveImageIdMismatch();
error ProveBlockNumberMismatch();
error ProveOutputRootMismatch();
error ParentCheckpointNumberMismatch();
error ParentCheckpointOutputRootMismatch();
error ProveVerifyFailed();
error InvalidJournal();
error NoAlert();
error NotOperator();

contract AutomationServiceManager is
    IAutomationServiceManager,
    ServiceManagerBase,
    AutomationServiceManagerStorage,
    BLSSignatureChecker,
    Pausable
{
    using EnumerableSet for EnumerableSet.Bytes32Set;
    using EnumerableSet for EnumerableSet.AddressSet;

    modifier onlyWhitelister() {
        if (_msgSender() != whitelister) {
            revert NotWhitelister();
        }
        _;
    }

    constructor(
        IAVSDirectory __avsDirectory,
        IRegistryCoordinator __registryCoordinator,
        IStakeRegistry __stakeRegistry
    )
        BLSSignatureChecker(__registryCoordinator)
        ServiceManagerBase(
            __avsDirectory,
            __registryCoordinator,
            __stakeRegistry
        )
    {
        _disableInitializers();
    }

    function initialize(
        IPauserRegistry _pauserRegistry,
        uint _initialPausedStatus,
        address _initialOwner,
        address _whitelister
    ) public initializer {
        _initializePauser(_pauserRegistry, _initialPausedStatus);
        __ServiceManagerBase_init(_initialOwner);
        _setWhitelister(_whitelister);
    }

    function setWhitelister(address whitelister) external onlyOwner {
        _setWhitelister(whitelister);
    }

    //////////////////////////////////////////////////////////////////////////////
    //                          Operator Registration                           //
    //////////////////////////////////////////////////////////////////////////////

    /**
     * @notice Register an operator with the AVS. Forwards call to EigenLayer' AVSDirectory.
     * @param operator The address of the operator to register.
     * @param operatorSignature The signature, salt, and expiry of the operator's signature.
     */
    function registerOperatorToAVS(
        address operator,
        ISignatureUtils.SignatureWithSaltAndExpiry memory operatorSignature
    )
        public
        override(ServiceManagerBase, IServiceManager)
        whenNotPaused
        onlyRegistryCoordinator
    {
        // we don't check if this operator has registered or not as AVSDirectory has such checking already
        _operators.add(operator);
        // Stake requirement for quorum is checked in StakeRegistry.sol
        // https://github.com/Layr-Labs/eigenlayer-middleware/blob/dev/src/RegistryCoordinator.sol#L488
        // https://github.com/Layr-Labs/eigenlayer-middleware/blob/dev/src/StakeRegistry.sol#L84
        _avsDirectory.registerOperatorToAVS(operator, operatorSignature);
        emit OperatorAdded(operator);
    }

    /**
     * @notice Deregister an operator from the AVS. Forwards a call to EigenLayer's AVSDirectory.
     * @param operator The address of the operator to register.
     */
    function deregisterOperatorFromAVS(
        address operator
    )
        public
        override(ServiceManagerBase, IServiceManager)
        whenNotPaused
        onlyRegistryCoordinator
    {
        _operators.remove(operator);
        _avsDirectory.deregisterOperatorFromAVS(operator);
        emit OperatorRemoved(operator);
    }

    function _setWhitelister(address _whitelister) internal {
        address previousWhitelister = whitelister;
        whitelister = _whitelister;
    }

    function setTaskManager(address _newTaskManager) external onlyOwner {
        address previousTaskManager = address(automationTaskManager);
        automationTaskManager = IAutomationTaskManager(_newTaskManager);
        emit TaskManagerUpdate(
            address(automationTaskManager),
            previousTaskManager
        );
    }

    /**
     * @notice Sets the slasher address
     * @param _slasher The address of the slasher contract
     */
    function setSlasher(address _slasher) external onlyOwner {
        address previousSlasher = slasher;
        slasher = _slasher;
        
        emit SlasherUpdated(slasher, previousSlasher);
    }
}
