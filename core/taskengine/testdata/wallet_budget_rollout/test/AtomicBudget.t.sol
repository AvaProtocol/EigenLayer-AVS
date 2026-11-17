// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.26;

interface Vm {
    function envString(string calldata) external returns (string memory);
    function envBytes(string calldata) external returns (bytes memory);
    function envUint(string calldata) external returns (uint256);
    function envAddress(string calldata) external returns (address);
    function createSelectFork(string calldata, uint256) external returns (uint256);
    function addr(uint256) external returns (address);
    function prank(address) external;
    function deal(address, uint256) external;
    function etch(address, bytes calldata) external;
    function sign(uint256, bytes32) external returns (uint8, bytes32, bytes32);
}

struct Call {
    address target;
    uint256 value;
    bytes data;
}

struct UserOp {
    address sender;
    uint256 nonce;
    bytes initCode;
    bytes callData;
    bytes32 accountGasLimits;
    uint256 preVerificationGas;
    bytes32 gasFees;
    bytes paymasterAndData;
    bytes signature;
}

struct ValidationData {
    uint8 flags;
    bytes25[] validationHooks;
    bytes25[] executionHooks;
    bytes4[] selectors;
}

interface Factory {
    function SEMI_MODULAR_ACCOUNT_IMPL() external view returns (address);
    function createSemiModularAccount(address, uint256) external payable returns (address);
}

interface Account {
    function executeBatch(Call[] calldata) external payable;
    function getValidationData(bytes24) external view returns (ValidationData memory);
}

interface Token {
    function allowance(address, address) external view returns (uint256);
}

interface Signer {
    function signers(uint32, address) external view returns (address);
}

interface Allowlist {
    function erc20SpendLimits(uint32, address, address) external view returns (uint256);
}

interface EntryPoint {
    function getNonce(address, uint192) external view returns (uint256);
    function getUserOpHash(UserOp calldata) external view returns (bytes32);
    function handleOps(UserOp[] calldata, address payable) external;
}

contract RejectingApproval {
    function approve(address, uint256) external pure returns (bool) {
        revert("fixture reset refused");
    }
}

// RPC is read-only. All deployments and signed handleOps calls run on a fork.
contract AtomicBudgetTest {
    Vm constant vm = Vm(address(uint160(uint256(keccak256("hevm cheat code")))));
    address constant FACTORY = 0x00000000000017c61b5bEe81050EC8eFc9c6fecd;
    address constant IMPL = 0x000000000000c5A9089039570Dd36455b5C07383;
    address constant SIGNER = 0x00000000000099DE0BF6fA90dEB851E2A2df7d83;
    address constant ALLOWLIST = 0x00000000003e826473A313E600b5b9B791f5a59a;
    address constant ENTRYPOINT = 0x0000000071727De22E5E9d8BAf0edAc6f37da032;
    address constant SPENDER = 0x2222222222222222222222222222222222222222;
    uint256 constant OWNER_KEY = 0xA11CE;
    uint256 constant SESSION_KEY = 0xB0B;

    function testAtomicBudgetMainnet() external {
        vm.createSelectFork(vm.envString("BUDGET_FORK_RPC"), vm.envUint("BUDGET_FORK_BLOCK"));
        uint256 chain = vm.envUint("BUDGET_FORK_CHAIN");
        require((chain == 1 || chain == 8453) && block.chainid == chain, "wrong network");
        require(Factory(FACTORY).SEMI_MODULAR_ACCOUNT_IMPL() == IMPL, "implementation changed");
        address owner = vm.addr(OWNER_KEY);
        address wallet = Factory(FACTORY).createSemiModularAccount(owner, vm.envUint("BUDGET_FORK_SALT"));
        require(wallet == vm.envAddress("BUDGET_FORK_WALLET"), "Go/fork factory derivation differs");
        vm.deal(wallet, 1 ether);
        vm.etch(address(0xF00D), address(new RejectingApproval()).code);
        (address[] memory tokens, bytes[] memory good, bytes[] memory badReset, bytes[] memory badInstall) =
            abi.decode(vm.envBytes("BUDGET_FORK_CASES"), (address[], bytes[], bytes[], bytes[]));
        for (uint256 i; i < tokens.length; ++i) {
            uint32 entity = uint32(i + 7);
            _seed(owner, wallet, tokens[i]);
            vm.prank(owner);
            (bool resetOK, bytes memory reason) = wallet.call(badReset[i]);
            require(!resetOK && _contains(reason, bytes("fixture reset refused")), "wrong reset refusal");
            _unchanged(wallet, tokens[i], entity);
            vm.prank(owner);
            (bool installOK,) = wallet.call(badInstall[i]);
            require(!installOK, "invalid install accepted");
            _unchanged(wallet, tokens[i], entity);

            // Exact Go-encoded budget batch, signed by the owner through the
            // real EntryPoint rather than applying the install with a prank.
            _send(wallet, 0, OWNER_KEY, good[i]);
            require(Token(tokens[i]).allowance(wallet, SPENDER) == 0, "approval not reset");
            require(
                Allowlist(ALLOWLIST).erc20SpendLimits(entity, tokens[i], wallet) == 500, "token budget not installed"
            );
            require(Signer(SIGNER).signers(entity, wallet) == vm.addr(SESSION_KEY), "signer not installed");
            require(
                Account(wallet).getValidationData(bytes24(abi.encodePacked(SIGNER, entity))).flags == 5,
                "validation missing"
            );

            Call[] memory calls = new Call[](1);
            calls[0] = Call(tokens[i], 0, abi.encodeWithSignature("approve(address,uint256)", SPENDER, 1));
            bytes memory operation = abi.encodePacked(bytes4(0x8dd7712f), abi.encodeCall(Account.executeBatch, (calls)));
            _send(wallet, entity, SESSION_KEY, operation);
            require(Token(tokens[i]).allowance(wallet, SPENDER) == 1, "session execution failed");
            require(
                Allowlist(ALLOWLIST).erc20SpendLimits(entity, tokens[i], wallet) == 499, "session spend not metered"
            );
        }
    }

    function _seed(address owner, address wallet, address token) internal {
        Call[] memory calls = new Call[](1);
        calls[0] = Call(token, 0, abi.encodeWithSignature("approve(address,uint256)", SPENDER, 123));
        vm.prank(owner);
        Account(wallet).executeBatch(calls);
        require(Token(token).allowance(wallet, SPENDER) == 123, "approval positive control failed");
    }

    function _unchanged(address wallet, address token, uint32 entity) internal view {
        require(Token(token).allowance(wallet, SPENDER) == 123, "earlier reset was not rolled back");
        require(Signer(SIGNER).signers(entity, wallet) == address(0), "failed batch installed authority");
    }

    function _send(address wallet, uint32 entity, uint256 key, bytes memory data) internal {
        UserOp[] memory ops = new UserOp[](1);
        UserOp memory op;
        op.sender = wallet;
        op.nonce = EntryPoint(ENTRYPOINT).getNonce(wallet, (uint192(entity) << 8) | 1);
        op.callData = data;
        op.accountGasLimits = bytes32((uint256(5000000) << 128) | 5000000);
        op.preVerificationGas = 100000;
        op.gasFees = bytes32((uint256(1) << 128) | 1);
        bytes32 hash = EntryPoint(ENTRYPOINT).getUserOpHash(op);
        (uint8 v, bytes32 r, bytes32 s) =
            vm.sign(key, keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", hash)));
        op.signature = abi.encodePacked(hex"ff00", r, s, v);
        ops[0] = op;
        EntryPoint(ENTRYPOINT).handleOps(ops, payable(address(this)));
    }

    function _contains(bytes memory haystack, bytes memory needle) internal pure returns (bool) {
        for (uint256 i; i + needle.length <= haystack.length; ++i) {
            bool matched = true;
            for (uint256 j; j < needle.length; ++j) {
                if (haystack[i + j] != needle[j]) {
                    matched = false;
                    break;
                }
            }
            if (matched) return true;
        }
        return false;
    }

    receive() external payable {}
}
