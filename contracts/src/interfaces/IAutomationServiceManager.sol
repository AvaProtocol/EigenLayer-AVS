// SPDX-License-Identifier: UNLICENSED
pragma solidity =0.8.12;

import {IServiceManager} from "@eigenlayer-middleware/interfaces/IServiceManager.sol";
import {BLSSignatureChecker} from "@eigenlayer-middleware/BLSSignatureChecker.sol";

/**
 * @title Interface for the MachServiceManager contract.
 * @author Altlayer, Inc.
 */
interface IAutomationServiceManager is IServiceManager {
    /**
     * @notice Emitted when an operator is added to the MachServiceManagerAVS.
     * @param operator The address of the operator
     */
    event OperatorAdded(address indexed operator);

    /**
     * @notice Emitted when an operator is removed from the MachServiceManagerAVS.
     * @param operator The address of the operator
     */
    event OperatorRemoved(address indexed operator);
}
