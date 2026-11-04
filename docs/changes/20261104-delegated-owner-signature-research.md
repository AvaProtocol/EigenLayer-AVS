# Research: a MetaMask-delegated owner of a derived MA v2 wallet

## Finding

The pinned `alchemy.sma-bytecode.1.0.0` fallback **UserOperation** validator accepts
the owner's existing raw-hash `personal_sign` recipe even when the owner EOA has
an EIP-7702 designation to another implementation. Signature type `0` recovers
the signer with ECDSA; it does not call the owner's code or ERC-1271. This is a
contract-validation result, not proof of every browser wallet's signing format
or of bundler acceptance of a complete operation.

Production support is unchanged. The gateway still rejects a MetaMask-delegated
owner under its existing pinned-SMA policy and now explains that refusal with
structured metadata. This document proposes a follow-up; it does not enable it.

## Source and bytecode evidence

Pinned source: Alchemy commit
[`bd75c9eca7ff5a66aaebc3cf2dab43ddc4622482`](https://github.com/alchemyplatform/modular-account/blob/bd75c9eca7ff5a66aaebc3cf2dab43ddc4622482/src/account/SemiModularAccountBase.sol).
`_execUserOpValidation` applies `userOpHash.toEthSignedMessageHash()` and invokes
`_checkSignature`. Its EOA branch (type `0`) uses `ECDSA.tryRecover` and compares
the recovered address with the fallback signer. Only the separate
`CONTRACT_OWNER` branch invokes `SignatureChecker.isValidERC1271SignatureNow`.
Code presence on the owner does not choose the signature type.

The derived-account implementation
`0x000000000000c5A9089039570Dd36455b5C07383` was read from a public Sepolia RPC
without signing or broadcasting. Its 24,223-byte runtime has keccak256
`0x3612f3fa5ac5e89b87b284024977e6082441e30ff05f9f8497cd280131a27e16`, matching the
existing owner-execution audit pin. The
[bytecode account](https://github.com/alchemyplatform/modular-account/blob/bd75c9eca7ff5a66aaebc3cf2dab43ddc4622482/src/account/SemiModularAccountBytecode.sol)
retrieves the fallback signer separately; that retrieval does not test owner code.

A scratch Go EVM experiment executed that exact runtime through a delegatecall
proxy with the owner set in its fallback storage slot. The owner EOA carried
`0xef0100 || 0x63c0c19a282a1B52b07dD5a65b58948A07DAE32B`, MetaMask's observed
delegate. The delegate was replaced in the local EVM with an always-reverting
runtime: if validation had called the owner, it could not have succeeded.

Seven cases passed: the normal 32-byte-hash personal signature validated;
bare-hash, hex-text and double-prefixed signatures failed; a non-owner nonce,
disabled fallback, and a changed fallback signer were refused. The experiment
does not execute MetaMask's implementation, deploy a wallet, or exercise a
bundler. It isolates the claim that this EOA signature branch ignores owner
code. Its storage override represents an enabled fallback signer, rather than
testing factory deployment or immutable-argument extraction again.

Local evidence is saved as `/tmp/budget-owner-delegated-research_test.go` and
`/tmp/budget-owner-delegated-research.log`. The scratch test is not production
code and is not added to the test suite.

## Proposed next scope

Allow a separately reviewed MetaMask-owner path only when **sender differs from
owner**, with the derived account's existing factory, implementation, fallback
owner, EntryPoint, nonce and funding checks intact. Continue pinning the observed
owner designation across prepare and first submit. Keep signature type `0`, the
same EIP-191 digest, and the same `0xFF 0x00` framing.

This proof does not support executing from the MetaMask EOA itself. A sender's
delegated implementation defines its execute and validation functions; an EOA
sender must still satisfy the pinned SMA-7702 path. Supporting MetaMask as a
sender needs a separate execution integration.

Before widening the owner policy, confirm the actual MetaMask extension/mobile
`personal_sign` result for a delegated owner and simulate a complete derived
account UserOperation through the bundler. A contract-signer signature or wrapped
signature must not silently enter this EOA recipe. Record the approval decision
and add permanent bytecode regression fixtures with the support change.
