pragma solidity ^0.8.12;

import "../interfaces/IAPConfig.sol";

contract APConfig is IAPConfig	 {
    // Mapping from operator address to alias address
    mapping(address => address) private operatorToAlias;
    mapping(address => address) private aliasToOperator;

    // Function to declare an alias for the operator
    function declareAlias(address _alias) external override {
        require(_alias != address(0), "Alias address cannot be the zero address");
        require(_alias != msg.sender, "Alias address cannot be the same with operator address");

        operatorToAlias[msg.sender] = _alias;
        aliasToOperator[_alias] = msg.sender;

        emit AliasDeclared(msg.sender, _alias);
    }

    // Function to undeclare an alias for the operator
    function undeclare() external override {
        require(aliasToOperator[msg.sender] != address(0), "No alias declared for this operator");

        delete operatorToAlias[aliasToOperator[msg.sender]];
        delete aliasToOperator[msg.sender];

        emit AliasUndeclared(msg.sender);
    }

    // Function to get the alias of an operator
    function getAlias(address _operator) external view override returns (address) {
        return operatorToAlias[_operator];
    }

    function getOperatorForAlias(address _alias) external view override returns (address) {
        return aliasToOperator[_alias];
    }
}
