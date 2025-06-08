const _ = require("lodash");
const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");
const { ethers } = require("ethers");
const { Wallet } = ethers;
const { UlidMonotonic } = require("id128");
const util = require("util");

const { TaskType, TaskTrigger } = require("./static_codegen/avs_pb");

// Load the protobuf definition
const packageDefinition = protoLoader.loadSync("../protobuf/avs.proto", {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true,
});

const env = process.env.ENV || "development";

console.log("Current environment is: ", env);
const privateKey = process.env.PRIVATE_KEY; // Make sure to provide your private key with or without the '0x' prefix

const config = {
  development: {
    AP_AVS_RPC: "localhost:2206",
    TEST_TRANSFER_TOKEN: "0x2e8bdb63d09ef989a0018eeb1c47ef84e3e61f7b",
    TEST_TRANSFER_TO: "0xe0f7D11FD714674722d325Cd86062A5F1882E13a",
    ORACLE_PRICE_CONTRACT: "0x694AA1769357215DE4FAC081bf1f309aDC325306",
    // on local development we still target smart wallet on sepolia
    RPC_PROVIDER: "https://sepolia.gateway.tenderly.co",
  },

  staging: {
    AP_AVS_RPC: "aggregator-holesky.avaprotocol.org:2206",
    TEST_TRANSFER_TOKEN: "0x2e8bdb63d09ef989a0018eeb1c47ef84e3e61f7b",
    TEST_TRANSFER_TO: "0xe0f7D11FD714674722d325Cd86062A5F1882E13a",
    ORACLE_PRICE_CONTRACT: "0x694AA1769357215DE4FAC081bf1f309aDC325306",
    RPC_PROVIDER: "https://sepolia.gateway.tenderly.co",
  },

  minato: {
    AP_AVS_RPC: "aggregator-minato.avaprotocol.org:2306",
    // https://explorer-testnet.soneium.org/token/0xBA33747043d09868946978Dd935130490a083458?tab=contract
    // anyone can mint this token for testing transfer it
    TEST_TRANSFER_TOKEN: "0xBA33747043d09868946978Dd935130490a083458",
    // Can be any arbitrary address to demonstrate that this address will receive the token above
    TEST_TRANSFER_TO: "0xa5ABB97A2540E4A4756E33f93fB2D7987668396a",
    ORACLE_PRICE_CONTRACT: "0x0ee7f0f7796Bd98c0E68107c42b21F5B7C13bcA9",
    RPC_PROVIDER: "https://rpc.minato.soneium.org",
  },

  production: {
    AP_AVS_RPC: "aggregator.avaprotocol.org:2206",
    TEST_TRANSFER_TOKEN: "0x72d587b34f7d21fbc47d55fa3d2c2609d4f25698",
    TEST_TRANSFER_TO: "0xa5ABB97A2540E4A4756E33f93fB2D7987668396a",
    ORACLE_PRICE_CONTRACT: "0x360B0a3f9Fc28Eb2426fa2391Fd2eB13912E1e40",
    RPC_PROVIDER: "https://mainnet.gateway.tenderly.co",
  },
};

const protoDescriptor = grpc.loadPackageDefinition(packageDefinition);
const apProto = protoDescriptor.aggregator;

// Enhanced client creation with timeout options
function createClient(serverAddress, credentials, options = {}) {
  const {
    timeout = 30000, // Default 30 seconds
    maxRetries = 3,
    retryDelay = 1000
  } = options;

  const client = new apProto.Aggregator(serverAddress, credentials);
  
  // Store timeout configuration on client for later use
  client._timeoutConfig = {
    timeout,
    maxRetries,
    retryDelay
  };
  
  return client;
}

const client = createClient(
  config[env].AP_AVS_RPC,
  grpc.credentials.createInsecure(),
  {
    timeout: 30000, // 30 seconds default
    maxRetries: 3,
    retryDelay: 1000
  }
);

// Enhanced asyncRPC function with timeout support
function asyncRPC(client, method, request, metadata, options = {}) {
  return new Promise((resolve, reject) => {
    const {
      timeout = client._timeoutConfig?.timeout || 30000,
      retries = client._timeoutConfig?.maxRetries || 3,
      retryDelay = client._timeoutConfig?.retryDelay || 1000
    } = options;

    let attempt = 0;
    
    function executeRequest() {
      attempt++;
      
      // Create a timeout promise
      const timeoutPromise = new Promise((_, timeoutReject) => {
        setTimeout(() => {
          timeoutReject(new Error(`gRPC request timeout after ${timeout}ms for method ${method}`));
        }, timeout);
      });

      // Create the actual gRPC call promise
      const grpcPromise = new Promise((grpcResolve, grpcReject) => {
        const call = client[method].bind(client)(request, metadata, (error, response) => {
          if (error) {
            grpcReject(error);
          } else {
            grpcResolve(response);
          }
        });

        // Handle call cancellation on timeout
        timeoutPromise.catch(() => {
          if (call && call.cancel) {
            call.cancel();
          }
        });
      });

      // Race between timeout and actual call
      Promise.race([grpcPromise, timeoutPromise])
        .then(resolve)
        .catch((error) => {
          const isTimeoutError = error.message.includes('timeout');
          const isRetryableError = isTimeoutError || 
            error.code === grpc.status.UNAVAILABLE || 
            error.code === grpc.status.DEADLINE_EXCEEDED ||
            error.code === grpc.status.RESOURCE_EXHAUSTED;

          if (isRetryableError && attempt < retries) {
            console.warn(`gRPC ${method} attempt ${attempt} failed, retrying in ${retryDelay}ms:`, error.message);
            setTimeout(executeRequest, retryDelay);
          } else {
            // Add timeout context to error
            if (isTimeoutError) {
              error.isTimeout = true;
              error.attemptsMade = attempt;
            }
            reject(error);
          }
        });
    }

    executeRequest();
  });
}

async function signMessageWithEthers(wallet, message) {
  const signature = await wallet.signMessage(message);
  return signature;
}

async function generateApiToken() {
  // When running from frontend, we will ask user to sign this through their
  // wallet providr
  const wallet = new Wallet(privateKey);
  const owner = wallet.address;
  const expired_at = Math.floor((+new Date() / 3600) * 24);
  const message = `key request for ${wallet.address} expired at ${expired_at}`;
  const signature = await signMessageWithEthers(wallet, message);
  //console.log(`message: ${message}\nsignature: ${signature}`)

  let result = await asyncRPC(
    client,
    "GetKey",
    {
      owner,
      expired_at,
      signature,
    },
    {}
  );

  return { owner, token: result.key };
}

async function listTask(owner, token) {
  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  const result = await asyncRPC(
    client,
    "ListTasks",
    {
      smart_wallet_address: process.argv[3].split(","),
      cursor: process.argv[4] || "",
      item_per_page: 2,
    },
    metadata
  );
  console.log(`Found ${result.items.length} tasks created by`, process.argv[3]);

  for (const item of result.items) {
    console.log(util.inspect(item, { depth: 4, colors: true }));
  }
  console.log(util.inspect({cursor: result.cursor, hasMore: result.has_more}, { depth: 4, colors: true }));
  console.log("Note: we are returning only 2 items per page to demonstrate pagination")
}

async function getTask(owner, token, taskId) {
  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  const result = await asyncRPC(client, "GetTask", { id: taskId }, metadata);

  console.log(util.inspect(result, { depth: 4, colors: true }));
}

async function listExecutions(owner, token, ids) {
  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  const result = await asyncRPC(client, "ListExecutions", { task_ids: ids.split(","), cursor: process.argv[4] || "", item_per_page: 200 }, metadata);

  console.log(util.inspect(result, { depth: 4, colors: true }));
}

async function getExecution(owner, token, task, execId) {
  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  const result = await asyncRPC(client, "GetExecution", { task_id: task, execution_id: execId }, metadata);

  console.log(util.inspect(result, { depth: 4, colors: true }));
}


async function cancel(owner, token, taskId) {
  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  const result = await asyncRPC(
    client,
    "CancelTask",
    { id: taskId },
    metadata
  );

  console.log("Response:\n", result);
}

async function deleteTask(owner, token, taskId) {
  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  const result = await asyncRPC(
    client,
    "DeleteTask",
    { id: taskId },
    metadata
  );

  console.log("Response:\n", result);
}

async function triggerTask(owner, token, taskId, triggerMetadata) {
  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  console.log("triggermark", triggerMetadata)

  const result = await asyncRPC(
    client,
    "TriggerTask",
    // If want to run async, comment this line out
    //{ task_id: taskId, triggerMetadata, },
    { task_id: taskId, trigger_metadata: JSON.parse(triggerMetadata), is_blocking: true },
    metadata
  );

  console.log("request", { task_id: taskId, triggerMetadata })

  console.log("Response:\n", result);
}

async function getWallets(owner, token) {
  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  const walletsResp = await asyncRPC(
    client,
    "ListWallets",
    { },
    metadata
  );

  console.log(
    `Response:\n`,
    walletsResp
  );

  console.log("Fetching balances from RPC provider ...");

  // Update the provider creation
  const provider = new ethers.JsonRpcProvider(config[env].RPC_PROVIDER);

  // Get token balance
  const tokenAddress = config[env].TEST_TRANSFER_TOKEN;
  const tokenAbi = [
    "function balanceOf(address account) view returns (uint256)",
    "function decimals() view returns (uint8)",
    "function symbol() view returns (string)",
  ];
  const tokenContract = new ethers.Contract(tokenAddress, tokenAbi, provider);

  let wallets = [];
  for (const wallet of walletsResp.items) {
    const balance = await provider.getBalance(wallet.address);
    const balanceInEth = _.floor(ethers.formatEther(balance), 2);

    const tokenBalance = await tokenContract.balanceOf(wallet.address);

    const tokenDecimals = await tokenContract.decimals();
    const tokenSymbol = await tokenContract.symbol();
    const tokenBalanceFormatted = _.floor(
      ethers.formatUnits(tokenBalance, tokenDecimals),
      2
    );
    wallets.push({
      ...wallet,
      balances: [
        `${balanceInEth} ETH`,
        `${tokenBalanceFormatted} ${tokenSymbol}`,
      ],
    });
  }
  console.log(
    `Listing smart wallet addresses for ${owner} ...\n`,
    wallets
  );

  return wallets;
}

const createWallet = async (owner, token, salt, factoryAddress) => {
  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  return await asyncRPC(
    client,
    "GetWallet",
    { salt, factoryAddress },
    metadata
  );
};

async function testRunNodeWithInputs(owner, token) {
  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  console.log("Testing RunNodeWithInputs with blockTrigger (using fast timeout)...");

  // Test blockTrigger node with fast timeout
  const blockTriggerRequest = {
    node_type: "blockTrigger",
    node_config: {
      interval: { kind: "numberValue", numberValue: 5 }
    },
    input_variables: {
      currentBlock: { kind: "numberValue", numberValue: 12345 }
    }
  };

  try {
    // Use fast timeout for quick operations
    const result = await fastRPC(
      client,
      "RunNodeWithInputs",
      blockTriggerRequest,
      metadata
    );

    console.log("BlockTrigger test result:");
    console.log(util.inspect(result, { depth: 4, colors: true }));
    
    if (result.success) {
      console.log("✅ BlockTrigger test passed!");
    } else {
      console.log("❌ BlockTrigger test failed:", result.error);
    }
  } catch (error) {
    if (error.isTimeout) {
      console.log(`❌ BlockTrigger test timed out after ${error.attemptsMade} attempts:`, error.message);
    } else {
      console.log("❌ BlockTrigger test error:", error.message);
    }
  }

  // Test customCode node with custom timeout
  console.log("\nTesting RunNodeWithInputs with customCode (using custom timeout)...");
  
  const customCodeRequest = {
    node_type: "customCode",
    node_config: {
      lang: { kind: "numberValue", numberValue: 0 }, // JavaScript = 0
      source: { kind: "stringValue", stringValue: "42" }
    },
    input_variables: {
      testVar: { kind: "stringValue", stringValue: "hello world" }
    }
  };

  try {
    // Use custom timeout for this specific operation
    const result = await asyncRPC(
      client,
      "RunNodeWithInputs",
      customCodeRequest,
      metadata,
      { timeout: 15000, retries: 1, retryDelay: 500 } // 15s timeout, 1 retry
    );

    console.log("CustomCode test result:");
    console.log(util.inspect(result, { depth: 4, colors: true }));
    
    if (result.success) {
      console.log("✅ CustomCode test passed!");
    } else {
      console.log("❌ CustomCode test failed:", result.error);
    }
  } catch (error) {
    if (error.isTimeout) {
      console.log(`❌ CustomCode test timed out after ${error.attemptsMade} attempts:`, error.message);
    } else {
      console.log("❌ CustomCode test error:", error.message);
    }
  }
}

const main = async (cmd) => {
  // 1. Generate the api token to interact with aggregator
  const { owner, token } = await generateApiToken();

  let taskCondition = "";

  switch (cmd) {
    case "create-wallet":
      salt = process.argv[3] || 0;
      let smartWalletAddress = await createWallet(
        owner,
        token,
        process.argv[3],
        process.argv[4]
      );
      console.log(
        `A new smart wallet with salt ${salt} is created for ${owner}:\nResponse:\n`,
        smartWalletAddress
      );
      break;
    case "schedule-monitor":
      scheduleMonitor(owner, token, process.argv[3]);
      break;
    case "schedule-aave":
      scheduleAaveMonitor(owner, token);
      break;
    case "schedule":
    case "schedule-cron":
    case "schedule-event":
    case "schedule-fixed":
    case "schedule-manual":
      // ETH-USD pair on sepolia
      // https://sepolia.etherscan.io/address/0x694AA1769357215DE4FAC081bf1f309aDC325306#code
      // The price return is big.Int so we have to use the cmp function to compare
      taskCondition = `
      bigCmp(
        priceChainlink("${config[env].ORACLE_PRICE_CONTRACT}"),
        toBigInt("10000")
      ) > 0`;
      const resultSchedule = await scheduleNotification(
        owner,
        token,
        taskCondition
      );
      console.log("Response: \n", resultSchedule);
      break;

    case "schedule2":
      taskCondition = `bigCmp(
      priceChainlink("${config[env].ORACLE_PRICE_CONTRACT}"),
      toBigInt("99228171987813")) > 0`;
      const resultSchedule2 = await scheduleNotification(
        owner,
        token,
        taskCondition
      );
      console.log("Response: \n", resultSchedule2);
      break;

    case "schedule-generic":
      // https://sepolia.etherscan.io/address/0x9aCb42Ac07C72cFc29Cd95d9DEaC807E93ada1F6#writeContract
      // This is a demo contract where we have a map of address -> number
      // we can set the value to demo that the task is trigger when the number
      // match a condition
      // When matching an arbitrary contract, the user need to provide the ABI
      // for the function they call so our task engine can unpack the result
      taskCondition = `
        bigCmp(
          readContractData(
            // Target contract address
            "0x9aCb42Ac07C72cFc29Cd95d9DEaC807E93ada1F6",
            // encoded call data for retrieve method, check getTaskDataQuery to
            // see how to generate this
            "0x0a79309b000000000000000000000000e272b72e51a5bf8cb720fc6d6df164a4d5e321c5",
            // Method call and ABI are needed so the engine can parse the result
            "retrieve",
            '[{"inputs":[{"internalType":"address","name":"addr","type":"address"}],"name":"retrieve","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"}]'
          )[0],
          toBigInt("2000")
        ) > 0`;
      const resultScheduleGeneric = await scheduleNotification(
        owner,
        token,
        taskCondition
      );

      console.log("Response: \n", resultScheduleGeneric);
      break;

    case "tasks":
      await listTask(owner, token);
      break;

    case "get":
      await getTask(owner, token, process.argv[3]);
      break;

    case "executions":
      await listExecutions(owner, token, process.argv[3]);
      break;
    case "execution":
      await getExecution(owner, token, process.argv[3], process.argv[4]);
      break;
    case "cancel":
      await cancel(owner, token, process.argv[3]);
      break;
    case "delete":
      await deleteTask(owner, token, process.argv[3]);
      break;
    case "wallet":
      await getWallets(owner, token);
      break;

    case "genTaskData":
      console.log("pack contract call", getTaskDataQuery(owner));
      break;

    case "time-schedule":
      await scheduleTimeTransfer(owner, token);
      break;

    case "trigger":
      await triggerTask(owner, token, process.argv[3], process.argv[4]);
      break;
    case "test-run-node":
      await testRunNodeWithInputs(owner, token);
      break;
    default:
      console.log(`Usage:

      create-wallet <salt> <factory-address(optional)>:        to create a smart wallet with a salt, and optionally a factory contract
      wallet:                                                  to list smart wallet address that has been created. note that a default wallet with salt=0 will automatically created
      tasks <smart-wallet-address>,<another-smart-wallet>,...: to list all tasks of given smart wallet address
      get <task-id>:                                           to get task detail. a permission error is throw if the eoa isn't the smart wallet owner.
      executions <task-id>:                                    to get task execution history. a permission error is throw if the eoa isn't the smart wallet owner.
      execution <task-id> <executio-id>:                       to get a single task execution history. a permission error is throw if the eoa isn't the smart wallet owner.
      schedule <smart-wallet-address>:                         to schedule a task that run on every block, with chainlink eth-usd its condition will be matched quickly
      schedule-cron <smart-wallet-address>:                    to schedule a task that run on cron
      schedule-event <smart-wallet-address>:                   to schedule a task that run on occurenct of an event
      schedule-generic:                                        to schedule a task with an arbitrary contract query
      schedule-aave:                                           monitor and report aavee liquidity rate every block
      monitor-address <wallet-address>:                        to monitor erc20 in/out for an address
      trigger <task-id> <trigger-mark>:                        manually trigger a task. Example:
                                                                 trigger abcdef '{"block_number":1234}' for blog trigger
                                                                 trigger abcdef '{"block_number":1234, "log_index":312,"tx_hash":"0x123"}' for event trigger
                                                                 trigger abcdef '{"epoch":1234, "log_index":312,"tx_hash":"0x123"}' for time based trigger (fixed or cron)
      test-run-node:                                           test the RunNodeWithInputs gRPC method with blockTrigger and customCode nodes
      cancel <task-id>:                                        to cancel a task
      delete <task-id>:                                        to completely remove a task`);
  }
};

function getTaskData() {
  let ABI = ["function transfer(address to, uint amount)"];
  let iface = new ethers.Interface(ABI);
  return iface.encodeFunctionData("transfer", [
    config[env].TEST_TRANSFER_TO,
    ethers.parseUnits("12", 18),
  ]);
}

function getTaskDataQuery(owner) {
  let ABI = ["function retrieve(address addr) public view returns (uint256)"];
  let iface = new ethers.Interface(ABI);
  return iface.encodeFunctionData("retrieve", [owner]);
}

async function scheduleNotification(owner, token, taskCondition) {
  // Now we can schedule a task
  // 1. Generate the calldata to check condition
  const taskBody = getTaskData();
  const smartWalletAddress = process.argv[3];
  if (!smartWalletAddress) {
    console.log("invalid smart wallet address. check usage");
    return;
  }

  console.log("Task body:", taskBody);

  console.log("\nTask condition:", taskCondition);

  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  let trigger = {
    block: {
      interval: 2, // run every 5 block
    },
  };

  if (process.argv[2] == "schedule-cron") {
    trigger = {
      cron: {
        schedule: [
          // every 5 hours
          "0 */5 * * *",
        ],
      },
    };
  } else if (process.argv[2] == "schedule-event") {
    trigger = {
      event: {
        expression: taskCondition,
      },
    };
  } else if (process.argv[2] == "schedule-fixed") {
    trigger = {
      fixed_time: {
        epochs: [
          Math.floor(new Date().getTime() / 1000 + 3600),
          Math.floor(new Date().getTime() / 1000 + 7200),
        ],
      },
    };
  } else if (process.argv[2] == "schedule-manual") {
    trigger = {
      manual: true,
    };
  }

  const nodeIdOraclePrice = UlidMonotonic.generate().toCanonical();
  const nodeIdTransfer = UlidMonotonic.generate().toCanonical();
  const nodeIdNotification = UlidMonotonic.generate().toCanonical();
  const branchIdLinkPriceHit = UlidMonotonic.generate().toCanonical();

  const result = await asyncRPC(
    client,
    "CreateTask",
    {
      smart_wallet_address: smartWalletAddress,
      nodes: [{
        id: nodeIdOraclePrice,
        name: 'check price',
        branch: {
          conditions: [{
            id: branchIdLinkPriceHit,
            type: "if",
            expression: `
              bigGt(
                // link token
                chainlinkPrice("${config[env].ORACLE_PRICE_CONTRACT}"),
                toBigInt("10000")
              )`,
          }]
        }
      }, {
        // id need to be unique. it will be assign to the variable
        id: nodeIdTransfer,
        // name is for our note only. use for display a humand friendly version
        name: 'transfer token',
        contract_write: {
          // Our ERC20 test token
          contract_address: config[env].TEST_TRANSFER_TOKEN,
          call_data: taskBody,
        }
      }, {
        id: nodeIdNotification,
        name: 'notification',
        rest_api: {
          // Visit https://webhook.site/#!/view/ca416047-5ba0-4485-8f98-76790b63add7 to see the request history
          url: "https://webhook.site/ca416047-5ba0-4485-8f98-76790b63add7",
        }
      }],

      edges: [
        {
          id: UlidMonotonic.generate().toCanonical(),
          // __TRIGGER__ is a special node. It doesn't appear directly in the task data, but it should be draw on the UI to show what is the entrypoint
          source: "__TRIGGER__",
          target: nodeIdOraclePrice,
        },
        {
          id: UlidMonotonic.generate().toCanonical(),
          // __trigger__ is a special node. It doesn't appear directly in the task nodes, but it should be draw on the UI to show what is the entrypoint
          source: `${nodeIdOraclePrice}.${branchIdLinkPriceHit}`,
          target: nodeIdNotification,
        },
      ],

      trigger,
      start_at: Math.floor(Date.now() / 1000) + 30,
      expired_at: Math.floor(Date.now() / 1000 + 3600 * 24 * 30),
      memo: `Demo Example task for ${owner}`,
    },
    metadata
  );

  return result;
}

// Utility functions for common timeout scenarios
const TimeoutPresets = {
  FAST: { timeout: 5000, retries: 2, retryDelay: 500 },     // 5s for quick operations
  NORMAL: { timeout: 30000, retries: 3, retryDelay: 1000 }, // 30s for normal operations  
  SLOW: { timeout: 120000, retries: 2, retryDelay: 2000 },  // 2min for heavy operations
  NO_RETRY: { timeout: 30000, retries: 0, retryDelay: 0 }   // No retries
};

// Convenience wrapper functions
function fastRPC(client, method, request, metadata) {
  return asyncRPC(client, method, request, metadata, TimeoutPresets.FAST);
}

function slowRPC(client, method, request, metadata) {
  return asyncRPC(client, method, request, metadata, TimeoutPresets.SLOW);
}

function noRetryRPC(client, method, request, metadata) {
  return asyncRPC(client, method, request, metadata, TimeoutPresets.NO_RETRY);
}

// setup a task to monitor in/out transfer for a wallet and send notification
async function scheduleMonitor(owner, token, target) {
  const wallets = await getWallets(owner, token);
  const smartWalletAddress = wallets[0].address;

  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  let trigger = {
    name: "trigger1",
    event: {
      // This is an example to show case the branch
      //
      // IN PRACTICE, it strongly recomend to add the filter directly to trigger to make it more efficient and not wasting aggregator resources
      // native eth transfer emit no event, we use this partciular topic[0] to simulate it
      // .. (trigger1.data.topic[0] == "native_eth_tx" && trigger1.data.topic[2] == "${target}" ) ||
      // TODO: eventually we want to allow trigger2 trigger3 but for now has to hardcode trigger1
      expression: `trigger1.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && trigger1.data.topics[2] == "${target.toLowerCase()}"`,
    },
  };

  const nodeIdNotification = UlidMonotonic.generate().toCanonical();
  const nodeIdCheckAmount = UlidMonotonic.generate().toCanonical();
  const branchIdCheckAmount = UlidMonotonic.generate().toCanonical();

  const result = await asyncRPC(
    client,
    "CreateTask",
    {
      smart_wallet_address: smartWalletAddress,
      nodes: [
        {
          id: nodeIdCheckAmount,
          name: 'checkAmount',
          branch: {
            conditions: [
              {
                id: branchIdCheckAmount,
                type: "if",
                expression: `
                   // usdc
                   ( trigger1.data.address == "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238" &&
                     bigGt(
                       toBigInt(trigger1.data.value),
                       toBigInt("2000000")
                     )
                   ) ||
                   ( trigger1.data.address == lower("0x779877a7b0d9e8603169ddbd7836e478b4624789") &&
                     bigGt(
                       // link token
                       chainlinkPrice("0xc59E3633BAAC79493d908e63626716e204A45EdF"),
                       toBigInt("5000000")
                     )
                   )
                `
              }
            ]
          },
        },
        {
          id: nodeIdNotification,
          name: 'notification',
          rest_api: {
            // As an user, they have 2 option to provide telegram bot token:
            // 1. Use their own bot by putting the token directly here. That user is the only one see their own tasks/logs
            // 2. (Prefered way) Use Ava Protocol Bot token. However, now because the user can see their own task config, we cannot use the raw token and has to use a variable here.
            url: "https://api.telegram.org/bot{{notify_bot_token}}/sendMessage?parse_mode=MarkdownV2",
            //url: `https://webhook.site/4a2cb0c4-86ea-4189-b1e3-ce168f5d4840`,
            method: "POST",
            //body: "chat_id=-4609037622&disable_notification=true&text=%2AWallet+${target.toLowerCase()}+receive+{{ trigger1.data.data }} {{ trigger1.data.token_symbol }} at {{ trigger1.data.tx_hash }}%2A",
            // This body is written this way so that it will be evaluate at run time in a JavaScript sandbox
            // It's important to quote amount with `` because it may contains a `.` and need to be escape with markdownv2
            body: `JSON.stringify({
              chat_id:-4609037622,
              text: \`Congrat, your walllet [\${trigger1.data.to_address}](https://sepolia.etherscan.io/address/\${trigger1.data.to_address}) received \\\`\${trigger1.data.value_formatted}\\\` [\${trigger1.data.token_symbol}](https://sepolia.etherscan.io/token/\${trigger1.data.address}) from \${trigger1.data.from_address} at [\${trigger1.data.transaction_hash}](sepolia.etherscan.io/tx/\${trigger1.data.transaction_hash})\`
            })`,
            headers: {
              "content-type": "application/json"
            }
          }
        },
      ],

      edges: [
        {
          id: UlidMonotonic.generate().toCanonical(),
          // __TRIGGER__ is a special node. It doesn't appear directly in the task data, but it should be draw on the UI to show what is the entrypoint
          source: "__TRIGGER__",
          target: nodeIdCheckAmount,
        },
        {
          id: UlidMonotonic.generate().toCanonical(),
          // __TRIGGER__ is a special node. It doesn't appear directly in the task data, but it should be draw on the UI to show what is the entrypoint
          source: `${nodeIdCheckAmount}.${branchIdCheckAmount}`,
          target: nodeIdNotification,
        },
      ],

      trigger,
      start_at: Math.floor(Date.now() / 1000) + 30,
      expired_at: Math.floor(Date.now() / 1000 + 3600 * 24 * 30),
      memo: `Montoring large token transfer for ${target}`,
    },
    metadata
  );

  console.log("create task", result);

  return result;
}

// setup a task to monitor in/out transfer for a wallet and send notification
async function scheduleAaveMonitor(owner, token) {
  const wallets = await getWallets(owner, token);
  const smartWalletAddress = wallets[0].address;

  const metadata = new grpc.Metadata();
  metadata.add("authkey", token);

  let trigger = {
    name: "trigger1",
    block: {
      interval: 1,
    },
  };

  const getReserveId = UlidMonotonic.generate().toCanonical();
  const sendSummaryId = UlidMonotonic.generate().toCanonical();
  const getIpId = UlidMonotonic.generate().toCanonical();

  const result = await asyncRPC(
    client,
    "CreateTask",
    {
      smart_wallet_address: smartWalletAddress,
      nodes: [
        {
          id: getReserveId,
          name: 'getReserveUSDC',
          graphql_query: {
            url: 'https://gateway.thegraph.com/api/10186dcf11921c7d1bc140721c69da38/subgraphs/id/Cd2gEDVeqnjBn1hSeqFMitw8Q1iiyV9FYUZkLNRcL87g',
            query: `
              {
                reserves(where: {underlyingAsset: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"}) {
                  id
                  underlyingAsset
                  name
                  decimals
                  liquidityRate
                  aToken {
                    id
                  }
                  sToken {
                    id
                  }
                }
              }
            `
          }
        },
        {
          id: getIpId,
          name: 'getIpAddress',
          rest_api: {
            url: 'https://ipinfo.io/json',
          }
        },

        {
          id: sendSummaryId,
          name: 'notification',
          rest_api: {
            url: "https://api.telegram.org/bot{{notify_bot_token}}/sendMessage?",
            //url: `https://webhook.site/ca416047-5ba0-4485-8f98-76790b63add7`,
            method: "POST",
            body: `JSON.stringify({
              chat_id:-4609037622,
              text: \`Node IP is: \${getIpAddress.data.ip}.\nCurrent USDC liquidity rate in RAY unit is \${getReserveUSDC.data.reserves[0].liquidityRate} \`
            })`,
            headers: {
              "content-type": "application/json"
            }
          }
        },
      ],

      edges: [
        {
          id: UlidMonotonic.generate().toCanonical(),
          // __TRIGGER__ is a special node. It doesn't appear directly in the task data, but it should be draw on the UI to show what is the entrypoint
          source: "__TRIGGER__",
          target: getIpId,
        },
        {
          id: UlidMonotonic.generate().toCanonical(),
          // __TRIGGER__ is a special node. It doesn't appear directly in the task data, but it should be draw on the UI to show what is the entrypoint
          source: getIpId,
          target: getReserveId,
        },
        {
          id: UlidMonotonic.generate().toCanonical(),
          // __TRIGGER__ is a special node. It doesn't appear directly in the task data, but it should be draw on the UI to show what is the entrypoint
          source: getReserveId,
          target: sendSummaryId,
        },
      ],

      trigger,
      start_at: Math.floor(Date.now() / 1000) + 30,
      expired_at: Math.floor(Date.now() / 1000 + 3600 * 24 * 30),
      memo: `Montoring USDC aavee on ethereum`,
    },
    metadata
  );

  console.log("create task", result);

  return result;
}


(async () => {
  try {
    main(process.argv[2]);
  } catch (e) {
    console.log("error from grpc", e.code, "detail", e.message);
  }
})();
