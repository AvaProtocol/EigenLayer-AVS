const { Wallet } = require('ethers')
const grpc = require('@grpc/grpc-js')
const protoLoader = require('@grpc/proto-loader')
const { ethers } = require("ethers")

// Load the protobuf definition
const packageDefinition = protoLoader.loadSync('../protobuf/avs.proto', {
    keepCase: true,
    longs: String,
    enums: String,
    defaults: true,
    oneofs: true
})

const protoDescriptor = grpc.loadPackageDefinition(packageDefinition)
const apProto = protoDescriptor.aggregator // Replace with your package name
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
  const taskCondition = `chainlinkPrice("0x694AA1769357215DE4FAC081bf1f309aDC325306") > parseUnit("262199799820", 8)`
  console.log("\n\nTask condition:", taskBody)

  const metadata = new grpc.Metadata();
  console.log("token is", token);
  metadata.add('authkey', token);

  const result = await asyncRPC(client, 'CreateTask', {
    task_type: apProto.TaskType.ContractExecutionTask,
    body: {
      contract_execution: {
        // Our ERC20 test token deploy on sepolia
        // https://sepolia.etherscan.io/token/0x69256ca54e6296e460dec7b29b7dcd97b81a3d55#code
        contract_address: "0x69256ca54e6296e460dec7b29b7dcd97b81a3d55",
        calldata: taskBody,
      }
    },

    trigger: {
      trigger_type: apProto.TriggerType.ExpressionTrigger,
      expression: {
        expression: taskCondition,
      },
    },
    
    expired_at: Math.floor(+new Date() / 3600 * 24 * 30),
    memo: `Demo Example task for ${owner}`
  }, metadata)

  console.log("Task creation resp", result)
}

(async() => {
  const { owner, token } = await generateApiToken();
  await scheduleExampleJob(owner, token)
})()
