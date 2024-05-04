pragma solidity ^0.8.12;

import { EnumerableSet } from '@openzeppelin/contracts/utils/structs/EnumerableSet.sol';

abstract contract AutomationServiceManagerStorage {
    // The number of blocks from the task initialization within which the aggregator has to respond to

    address public whitelister;

    EnumerableSet.AddressSet internal _operators;
}
