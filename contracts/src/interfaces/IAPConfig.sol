// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.9;

interface IAPConfig {
    // Event emitted when an alias is declared
    event AliasDeclared(address indexed operator, address indexed aliasAddress);

    // Event emitted when an alias is undeclared
    event AliasUndeclared(address indexed operator);

    // Function to declare an alias for the operator
    function declareAlias(address _alias) external;

    // Function to undeclare an alias for the operator
    function undeclare() external;

    // Function to get the alias of an operator
    function getAlias(address _operator) external view returns (address);
    function getOperatorForAlias(address _alias) external view returns (address);
}
