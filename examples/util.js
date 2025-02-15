const { ethers } = require("ethers");

// Your contract ABI
const abi = [
    "function balanceOf(address account) external view returns (uint256)",
    "function getRoundData(uint80 _roundId) view returns ( uint80 roundId, int256 answer, uint256 startedAt, uint256 updatedAt, uint80 answeredInRound)",
    "function latestRoundData() public view returns ( uint80 roundId, int256 answer, uint256 startedAt, uint256 updatedAt, uint80 answeredInRound)",
    "function transfer(address _to, uint _value)",
];

// Function to encode
const iface = new ethers.Interface(abi);
const call1 = iface.encodeFunctionData("getRoundData", ["18446744073709572839"]);
console.log("Encoded Call1 Data:", call1);
const call2 = iface.encodeFunctionData("balanceOf", ["0xce289bb9fb0a9591317981223cbe33d5dc42268d"]);
console.log("Encoded Call2 Data:", call2);
const call3 = iface.encodeFunctionData("latestRoundData");
console.log("Encoded Call3 Data:", call3);

const call4 = iface.encodeFunctionData("transfer", ["0xe0f7D11FD714674722d325Cd86062A5F1882E13a", 7621000000000000]);
console.log("Encoded Call4 Data:", call4);

