const { Wallet } = require('ethers');

async function signMessageWithEthers(wallet, message) {
    const signature = await wallet.signMessage(message);
    return signature;
}

const privateKey = process.env.PRIVATE_KEY; // Make sure to provide your private key with or without the '0x' prefix

const wallet = new Wallet(privateKey);
const message = `key request for ${wallet.address} expired at ${Math.floor(+new Date() / 3600 * 24)}`

signMessageWithEthers(wallet, message).then(signature => {
    console.log(`message: ${message}\nsignature: ${signature}`);
});
