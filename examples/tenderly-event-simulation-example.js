// Example: Using Tenderly Event Trigger Simulation for Chainlink Price Feeds
// This demonstrates the new simulationMode feature for EventTriggers

const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");

// Load the protobuf definition
const packageDefinition = protoLoader.loadSync("../protobuf/avs.proto", {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true,
});

const privateKey = process.env.PRIVATE_KEY;

const protoDescriptor = grpc.loadPackageDefinition(packageDefinition);
const apProto = protoDescriptor.aggregator;

// Configuration
const config = {
  // Sepolia configuration with Tenderly simulation
  sepolia: {
    AP_AVS_RPC: "aggregator-sepolia.avaprotocol.org:2206",
    CHAINLINK_ETH_USD: "0x694AA1769357215DE4FAC081bf1f309aDC325306", // Sepolia ETH/USD
    RPC_PROVIDER: "https://sepolia.gateway.tenderly.co",
  }
};

const currentConfig = config.sepolia;

// Create gRPC client
const client = new apProto.Aggregator(
  currentConfig.AP_AVS_RPC,
  grpc.credentials.createInsecure()
);

// Chainlink AnswerUpdated event signature
const CHAINLINK_ANSWER_UPDATED_SIGNATURE = "0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f";

// Chainlink Price Feed ABI for AnswerUpdated event
const CHAINLINK_AGGREGATOR_ABI = [
  {
    anonymous: false,
    inputs: [
      { indexed: true, internalType: "int256", name: "current", type: "int256" },
      { indexed: true, internalType: "uint256", name: "roundId", type: "uint256" },
      { indexed: false, internalType: "uint256", name: "updatedAt", type: "uint256" }
    ],
    name: "AnswerUpdated",
    type: "event"
  }
];

async function testTenderlyEventSimulation() {
  console.log("🔮 Testing Tenderly EventTrigger Simulation");
  console.log("📍 Using Sepolia ETH/USD price feed:", currentConfig.CHAINLINK_ETH_USD);

  // Example 1: Simple price monitoring without conditions
  console.log("\n=== Example 1: Basic Price Monitoring ===");
  try {
    const basicRequest = {
      triggerType: "eventTrigger",
      triggerConfig: {
        simulationMode: true, // 🔮 Enable Tenderly simulation
        queries: [
          {
            addresses: [currentConfig.CHAINLINK_ETH_USD],
            topics: [
              {
                values: [CHAINLINK_ANSWER_UPDATED_SIGNATURE]
              }
            ],
            contractAbi: JSON.stringify(CHAINLINK_AGGREGATOR_ABI),
            maxEventsPerBlock: 5
          }
        ]
      }
    };

    const response = await new Promise((resolve, reject) => {
      client.RunTrigger(basicRequest, (error, response) => {
        if (error) reject(error);
        else resolve(response);
      });
    });

    console.log("✅ Basic simulation successful!");
    
    if (response.event_trigger && response.event_trigger.evm_log) {
      console.log("📊 Event data found!");
      console.log("📍 Contract:", response.event_trigger.evm_log.address);
      
      // Parse debug information
      if (response.event_trigger.evm_log.debug_info) {
        const debugInfo = JSON.parse(response.event_trigger.evm_log.debug_info);
        console.log("💰 Current ETH Price: $" + debugInfo.real_price_usd);
        console.log("🔍 Debug Info:", debugInfo);
      }
    } else {
      console.log("❌ No event data returned");
    }
    
    console.log("📊 Full Response:", JSON.stringify(response, null, 2));

  } catch (error) {
    console.error("❌ Basic simulation failed:", error.message);
  }

  // Example 2: Conditional price monitoring (price > $2400)
  // This will use REAL current price from Tenderly and evaluate conditions against it
  console.log("\n=== Example 2: Price Alert Above $2400 (Real Data Test) ===");
  console.log("ℹ️  If real ETH price > $2400: found=true with event data");
  console.log("ℹ️  If real ETH price ≤ $2400: found=false (conditions not met)");
  try {
    const conditionalRequest = {
      triggerType: "eventTrigger", 
      triggerConfig: {
        simulationMode: true, // 🔮 Enable Tenderly simulation
        queries: [
          {
            addresses: [currentConfig.CHAINLINK_ETH_USD],
            topics: [
              {
                values: [CHAINLINK_ANSWER_UPDATED_SIGNATURE]
              }
            ],
            contractAbi: JSON.stringify(CHAINLINK_AGGREGATOR_ABI),
            conditions: [
              {
                fieldName: "current",         // The 'current' field from AnswerUpdated event
                operator: "gt",               // Greater than
                value: "240000000000",        // $2400 with 8 decimals (Chainlink format)
                fieldType: "int256"
              }
            ],
            maxEventsPerBlock: 5
          }
        ]
      }
    };

    const response = await new Promise((resolve, reject) => {
      client.RunTrigger(conditionalRequest, (error, response) => {
        if (error) reject(error);
        else resolve(response);
      });
    });

    if (response.event_trigger && response.event_trigger.evm_log) {
      console.log("✅ Conditions satisfied! Real ETH price > $2400");
      console.log("📊 Event data returned!");
      console.log("📍 Contract:", response.event_trigger.evm_log.address);
      
      // Parse debug information
      if (response.event_trigger.evm_log.debug_info) {
        const debugInfo = JSON.parse(response.event_trigger.evm_log.debug_info);
        console.log("💰 Current ETH Price: $" + debugInfo.real_price_usd);
        console.log("🔍 Debug Info:", JSON.stringify(debugInfo, null, 2));
      }
    } else {
      console.log("❌ Conditions NOT satisfied - Real ETH price ≤ $2400");
      console.log("📊 No event data returned (conditions not met)");
    }

  } catch (error) {
    console.error("❌ Conditional simulation failed:", error.message);
  }

  // Example 3: High threshold test (price > $4400) - Should likely fail
  console.log("\n=== Example 3: High Threshold Test ($4400) - Should Fail ===");
  console.log("ℹ️  This demonstrates realistic behavior - conditions must match real data");
  try {
    const highThresholdRequest = {
      triggerType: "eventTrigger",
      triggerConfig: {
        simulationMode: true, // 🔮 Enable Tenderly simulation
        queries: [
          {
            addresses: [currentConfig.CHAINLINK_ETH_USD],
            topics: [
              {
                values: [CHAINLINK_ANSWER_UPDATED_SIGNATURE]
              }
            ],
            contractAbi: JSON.stringify(CHAINLINK_AGGREGATOR_ABI),
            conditions: [
              {
                fieldName: "current",
                operator: "gt",               // Greater than
                value: "440000000000",        // $4400 with 8 decimals - unrealistic high
                fieldType: "int256"
              }
            ],
            maxEventsPerBlock: 5
          }
        ]
      }
    };

    const response = await new Promise((resolve, reject) => {
      client.RunTrigger(highThresholdRequest, (error, response) => {
        if (error) reject(error);
        else resolve(response);
      });
    });

    if (response.event_trigger && response.event_trigger.evm_log) {
      console.log("🚀 Wow! ETH is above $4400 right now!");
      console.log("📊 Event data found!");
      console.log("📍 Contract:", response.event_trigger.evm_log.address);
      
      // Parse debug information
      if (response.event_trigger.evm_log.debug_info) {
        const debugInfo = JSON.parse(response.event_trigger.evm_log.debug_info);
        console.log("💰 Current ETH Price: $" + debugInfo.real_price_usd);
        console.log("🔍 Debug Info:", JSON.stringify(debugInfo, null, 2));
      }
    } else {
      console.log("✅ Expected result: Conditions not met (ETH likely < $4400)");
      console.log("📊 No event data returned - real price doesn't meet threshold");
      console.log("🔍 Threshold was: $4400");
    }

  } catch (error) {
    console.error("❌ High threshold test failed:", error.message);
  }

  // Example 4: Compare with historical search (simulationMode: false)
  console.log("\n=== Example 4: Historical Search (No Simulation) ===");
  try {
    const historicalRequest = {
      triggerType: "eventTrigger",
      triggerConfig: {
        simulationMode: false, // 📊 Use historical blockchain search
        queries: [
          {
            addresses: [currentConfig.CHAINLINK_ETH_USD],
            topics: [
              {
                values: [CHAINLINK_ANSWER_UPDATED_SIGNATURE]
              }
            ],
            maxEventsPerBlock: 5
          }
        ]
      }
    };

    const response = await new Promise((resolve, reject) => {
      client.RunTrigger(historicalRequest, (error, response) => {
        if (error) reject(error);
        else resolve(response);
      });
    });

    console.log("✅ Historical search successful!");
    console.log("📜 Found real blockchain events");
    console.log("📊 Response:", JSON.stringify(response, null, 2));

  } catch (error) {
    console.error("❌ Historical search failed:", error.message);
  }
}

// Key differences between simulation and historical search:
console.log(`
🔮 TENDERLY SIMULATION MODE vs 📊 HISTORICAL SEARCH MODE

┌─────────────────────────────────────────────────────────────────────────────┐
│                           SIMULATION MODE                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│ ✅ simulationMode: true                                                     │
│ 🔮 Uses Tenderly Gateway to fetch REAL current price data                  │
│ ⚡ Instant results - no historical blockchain search needed                │
│ 💯 Evaluates conditions against REAL market data                          │
│ 🎯 Returns found=true ONLY if real data satisfies conditions              │
│ 📉 Returns found=false if conditions don't match real data                │
│ 🔍 Includes _raw field with debug info (real price, etc.)                 │
│ 💡 Perfect for realistic testing of event trigger conditions              │
│ 🧪 Shows exactly what runTask would return with current market data       │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│                         HISTORICAL SEARCH MODE                              │
├─────────────────────────────────────────────────────────────────────────────┤
│ 📊 simulationMode: false (default)                                         │
│ 🔍 Searches actual blockchain history for past events                     │
│ ⏳ May take time depending on how far back events occurred                 │
│ 💯 Returns real historical blockchain event data                           │
│ 🔒 Production-ready for live workflows                                     │
│ 📜 Shows events that actually happened and met conditions                 │
└─────────────────────────────────────────────────────────────────────────────┘

🚀 KEY BEHAVIOR: Both modes return identical protobuf structures
   - If conditions match: EventTrigger.Output with evm_log containing event data + debug_info
   - If conditions don't match: Empty EventTrigger.Output (no evm_log)
   - Simulation mode uses current real data, historical mode searches blockchain history
`);

// Run the example
if (require.main === module) {
  testTenderlyEventSimulation()
    .then(() => console.log("\n🎉 All examples completed!"))
    .catch(error => console.error("\n💥 Example failed:", error));
}

module.exports = { testTenderlyEventSimulation }; 