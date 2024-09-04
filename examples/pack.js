const { ethers } = require("ethers");

// Your struct values
const tokenAddress = "70EB4D3c164a6B4A5f908D4FBb5a9cAfFb66bAB6";
const weight = "997992210000000000";  // Example value

// Convert address to 20-byte hex
const encodedToken = ethers.getAddress(tokenAddress);

// Convert uint96 to 12-byte hex
const encodedWeight = ethers.toBeHex(weight, 12);

// Concatenate the two encoded values
const packedEncodedData = encodedToken + encodedWeight.slice(2);

console.log(packedEncodedData);

