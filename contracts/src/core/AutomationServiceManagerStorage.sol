pragma solidity ^0.8.12;

import { EnumerableSet } from '@openzeppelin/contracts/utils/structs/EnumerableSet.sol';
import { IAutomationTaskManager } from '../interfaces/IAutomationTaskManager.sol';

abstract contract AutomationServiceManagerStorage {
    IAutomationTaskManager public automationTaskManager;

    address public whitelister;
    address public slasher;

    EnumerableSet.AddressSet internal _operators;
}
