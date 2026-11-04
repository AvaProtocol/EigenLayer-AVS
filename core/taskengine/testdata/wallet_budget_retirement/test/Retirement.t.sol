// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.26;

interface Vm {
    function envString(string calldata) external returns (string memory);
    function envBytes(string calldata) external returns (bytes memory);
    function createSelectFork(string calldata) external returns (uint256);
    function addr(uint256) external returns (address);
    function prank(address) external;
    function etch(address, bytes calldata) external;
    function sign(uint256, bytes32) external returns (uint8, bytes32, bytes32);
}

struct Call {
    address target;
    uint256 value;
    bytes data;
}

struct PackedUserOperation {
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
    uint8 validationFlags;
    bytes25[] validationHooks;
    bytes25[] executionHooks;
    bytes4[] selectors;
}

struct AllowlistInput {
    address target;
    bool hasSelectorAllowlist;
    bool hasERC20SpendLimit;
    uint256 erc20SpendLimit;
    bytes4[] selectors;
}

interface Factory {
    function SEMI_MODULAR_ACCOUNT_IMPL() external view returns (address);
    function createSemiModularAccount(address, uint256) external payable returns (address);
}

interface Account {
    function installValidation(bytes25, bytes4[] calldata, bytes calldata, bytes[] calldata) external;
    function uninstallValidation(bytes24, bytes calldata, bytes[] calldata) external;
    function executeBatch(Call[] calldata) external payable;
    function getValidationData(bytes24) external view returns (ValidationData memory);
    function validateUserOp(PackedUserOperation calldata, bytes32, uint256) external returns (uint256);
}

interface SignerModule {
    function signers(uint32, address) external view returns (address);
}

interface AllowlistModule {
    function erc20SpendLimits(uint32, address, address) external view returns (uint256);
    function addressAllowlist(uint32, address, address) external view returns (bool, bool, bool);
    function selectorAllowlist(uint32, bytes4, address, address) external view returns (bool);
}

interface TimeModule {
    function timeRanges(uint32, address) external view returns (uint48, uint48);
}

interface NativeModule {
    function limits(uint256, address) external view returns (uint256);
}

interface EntryPoint {
    function getUserOpHash(PackedUserOperation calldata) external view returns (bytes32);
}

contract RevertingCleanup {
    function onUninstall(bytes calldata) external pure {
        revert("fixture cleanup refused");
    }
}

/**
 * Uses Sepolia's real factory, account implementation and stock modules.
 * A fork-only code replacement makes one hook's cleanup fail, then restores its
 * original code. No adversarial module is deployed to the public testnet.
 */
contract RetirementTest {
    Vm constant vm = Vm(address(uint160(uint256(keccak256("hevm cheat code")))));
    address constant FACTORY = 0x00000000000017c61b5bEe81050EC8eFc9c6fecd;
    address constant IMPL = 0x000000000000c5A9089039570Dd36455b5C07383;
    address constant SIGNER = 0x00000000000099DE0BF6fA90dEB851E2A2df7d83;
    address constant ALLOWLIST = 0x00000000003e826473A313E600b5b9B791f5a59a;
    address constant TIME = 0x00000000000082B8e2012be914dFA4f62A0573eA;
    address constant NATIVE = 0x00000000000001e541f0D090868FBe24b59Fbe06;
    address constant ENTRYPOINT = 0x0000000071727De22E5E9d8BAf0edAc6f37da032;
    address constant TOKEN = 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238;
    uint32 constant ENTITY = 7;
    uint256 constant SESSION_KEY = 0xB0B;

    function config(address module, uint8 flags) internal pure returns (bytes25) {
        return bytes25(uint200((uint256(uint160(module)) << 40) | (uint256(ENTITY) << 8) | flags));
    }

    function testRetirementAfterFailingHook() external {
        vm.createSelectFork(vm.envString("WALLET_BUDGET_FORK_RPC_URL"));
        require(block.chainid == 11155111, "Sepolia only");
        require(Factory(FACTORY).SEMI_MODULAR_ACCOUNT_IMPL() == IMPL, "implementation changed");
        address owner = vm.addr(0xA11CE);
        address session = vm.addr(SESSION_KEY);
        Account account = Account(Factory(FACTORY).createSemiModularAccount(owner, 0xB0D6E7));
        bytes24 entity = bytes24(abi.encodePacked(SIGNER, ENTITY));
        vm.prank(owner);
        (bool installed,) = address(account).call(vm.envBytes("WALLET_BUDGET_FORK_INSTALL"));
        require(installed, "Go-encoded install failed");
        require(account.getValidationData(entity).validationFlags == 5, "install did not register");

        // A real signature is accepted before removal; this rules out a malformed
        // nonce, signature or call as the reason for the later refusal.
        PackedUserOperation memory op;
        op.sender = address(account);
        op.nonce = uint256((uint192(ENTITY) << 8) | 1) << 64;
        op.callData = abi.encodePacked(bytes4(0x8dd7712f), abi.encodeCall(Account.executeBatch, (new Call[](0))));
        op.accountGasLimits = bytes32((uint256(1000000) << 128) | 1000000);
        op.preVerificationGas = 100000;
        op.gasFees = bytes32((uint256(1) << 128) | 1);
        bytes32 hash = EntryPoint(ENTRYPOINT).getUserOpHash(op);
        bytes32 digest = keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", hash));
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(SESSION_KEY, digest);
        op.signature = abi.encodePacked(hex"ff00", r, s, v);
        vm.prank(ENTRYPOINT);
        require(uint160(account.validateUserOp(op, hash, 0)) == 0, "positive signature control failed");

        bytes memory originalTimeCode = TIME.code;
        vm.etch(TIME, address(new RevertingCleanup()).code);
        bytes memory uninstall = vm.envBytes("WALLET_BUDGET_FORK_UNINSTALL");
        vm.prank(owner);
        (bool removed,) = address(account).call(uninstall);
        require(removed, "Go-encoded uninstall failed");
        ValidationData memory gone = account.getValidationData(entity);
        require(gone.validationFlags == 0 && gone.selectors.length == 0, "registration remained");
        require(gone.validationHooks.length == 0 && gone.executionHooks.length == 0, "hooks remained");
        require(SignerModule(SIGNER).signers(ENTITY, address(account)) == session, "missing residual signer control");
        require(
            AllowlistModule(ALLOWLIST).erc20SpendLimits(ENTITY, TOKEN, address(account)) == 500,
            "missing residual limit control"
        );
        require(NativeModule(NATIVE).limits(ENTITY, address(account)) > 0, "missing residual native limit control");

        vm.prank(ENTRYPOINT);
        (bool accepted, bytes memory reason) =
            address(account).call(abi.encodeCall(Account.validateUserOp, (op, hash, 0)));
        require(!accepted, "retired key retained authority");
        require(
            keccak256(reason)
                == keccak256(abi.encodeWithSignature("ValidationFunctionMissing(bytes4)", Account.executeBatch.selector)),
            "wrong refusal reason"
        );

        vm.etch(TIME, originalTimeCode);
        vm.prank(owner);
        (bool repeatUninstall,) = address(account).call(uninstall);
        require(!repeatUninstall, "old hook list unexpectedly reusable");
        require(SignerModule(SIGNER).signers(ENTITY, address(account)) == session, "repeat uninstall cleared residue");
        // With hook lists gone, do not call account.uninstallValidation again.
        // The owner has the ACCOUNT call each module's onUninstall directly.
        vm.prank(owner);
        (bool cleaned,) = address(account).call(vm.envBytes("WALLET_BUDGET_FORK_CLEANUP"));
        require(cleaned, "Go-encoded owner module cleanup failed");
        require(SignerModule(SIGNER).signers(ENTITY, address(account)) == address(0), "signer residue remained");
        require(
            AllowlistModule(ALLOWLIST).erc20SpendLimits(ENTITY, TOKEN, address(account)) == 0, "limit residue remained"
        );
        (bool allowed, bool selectorList, bool capped) =
            AllowlistModule(ALLOWLIST).addressAllowlist(ENTITY, TOKEN, address(account));
        require(!allowed && !selectorList && !capped, "address residue remained");
        require(
            !AllowlistModule(ALLOWLIST).selectorAllowlist(ENTITY, 0xa9059cbb, TOKEN, address(account)),
            "selector residue remained"
        );
        (uint48 until, uint48 after_) = TimeModule(TIME).timeRanges(ENTITY, address(account));
        require(until == 0 && after_ == 0, "time residue remained");
        require(NativeModule(NATIVE).limits(ENTITY, address(account)) == 0, "native residue remained");
    }
}
