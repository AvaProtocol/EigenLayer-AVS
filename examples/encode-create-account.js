import { keccak256, toUtf8Bytes } from "ethers";

// Compute the selector manually
const functionSignature = "createAccount(bytes[],uint256)";
const expectedSelector = keccak256(toUtf8Bytes(functionSignature)).slice(0, 10);
console.log("Expected Selector:", expectedSelector);
