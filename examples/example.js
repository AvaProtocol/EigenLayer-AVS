const { Wallet } = require('ethers')
const grpc = require('@grpc/grpc-js')
const protoLoader = require('@grpc/proto-loader')
const { ethers } = require("ethers")

const {
  TaskType, TriggerType
} = require('./static_codegen/avs_pb');

// Load the protobuf definition
const packageDefinition = protoLoader.loadSync('../protobuf/avs.proto', {
    keepCase: true,
    longs: String,
    enums: String,
    defaults: true,
    oneofs: true
})

const protoDescriptor = grpc.loadPackageDefinition(packageDefinition)
const apProto = protoDescriptor.aggregator
const client = new apProto.Aggregator('localhost:2206', grpc.credentials.createInsecure())

async function signMessageWithEthers(wallet, message) {
    const signature = await wallet.signMessage(message)
    return signature
}

const privateKey = process.env.PRIVATE_KEY // Make sure to provide your private key with or without the '0x' prefix

function asyncRPC(client, method, request, metadata) {
    return new Promise((resolve, reject) => {
		client[method].bind(client)(request, metadata, (error, response) => {
            if (error) {
                reject(error)
            } else {
                resolve(response)
            }
        })
    })
}

async function generateApiToken() {
  // When running from frontend, we will ask user to sign this through their
  // wallet providr
  const wallet = new Wallet(privateKey)
  const owner = wallet.address
  const expired_at = Math.floor(+new Date() / 3600 * 24)
  const message = `key request for ${wallet.address} expired at ${expired_at}`
  const signature = await signMessageWithEthers(wallet, message)
  console.log(`message: ${message}\nsignature: ${signature}`)

  let result = await asyncRPC(client, 'GetKey', {
      owner,
      expired_at,
      signature
  }, {})

  return { owner, token: result.key }
}

function getTaskData() {
  let ABI = [
    "function transfer(address to, uint amount)"
  ]
  let iface = new ethers.Interface(ABI)
  // 
  return iface.encodeFunctionData("transfer", [ "0xe0f7D11FD714674722d325Cd86062A5F1882E13a", ethers.parseUnits("0.00761", 18) ])
}

async function scheduleExampleJob(owner, token) {
  // Now we can schedule a task
  // 1. Generate the calldata to check condition
  const taskBody = getTaskData()
  console.log("\n\nTask body:", taskBody)

  // ETH-USD pair on sepolia
  // https://sepolia.etherscan.io/address/0x694AA1769357215DE4FAC081bf1f309aDC325306#code
  // The price return is big.Int so we have to use the cmp function to compare
  const taskCondition = `bigCmp(
      priceChainlink("0x694AA1769357215DE4FAC081bf1f309aDC325306"),
      toBigInt("228171987813")) > 0`
  console.log("\n\nTask condition:", taskCondition)

  const metadata = new grpc.Metadata();
  metadata.add('authkey', token);

  console.log("Trigger type", TriggerType.EXPRESSIONTRIGGER)

  const result = await asyncRPC(client, 'CreateTask', {
    // A contract execution will be perform for this taks
    task_type: TaskType.CONTRACTEXECUTIONTASK,

    body: {
      contract_execution: {
        // Our ERC20 test token deploy on sepolia
        // https://sepolia.etherscan.io/token/0x69256ca54e6296e460dec7b29b7dcd97b81a3d55#code
        contract_address: "0x69256ca54e6296e460dec7b29b7dcd97b81a3d55",
        calldata: taskBody,
      }
    },

    trigger: {
      trigger_type: TriggerType.EXPRESSIONTRIGGER,
      expression: {
        expression: taskCondition,
      },
    },
    
    start_at:  Math.floor(Date.now() / 1000) + 30,
    expired_at: Math.floor(Date.now() / 1000 + 3600 * 24 * 30),
    memo: `Demo Example task for ${owner}`
  }, metadata)

  console.log("Expression Task ID is:", result)
}

(async() => {
  // 1. Generate the api token to interact with aggregator
  const { owner, token } = await generateApiToken();

  // 2. Now we can schedule job for this owner
  await scheduleExampleJob(owner, token)
})()
