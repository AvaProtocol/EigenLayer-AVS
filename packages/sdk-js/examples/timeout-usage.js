const { createClient, ClientPresets, TimeoutPresets } = require('../src/index');

/**
 * Example usage of the Ava Protocol SDK with timeout functionality
 */
async function demonstrateTimeoutUsage() {
  console.log('🚀 Ava Protocol SDK - Timeout Usage Examples\n');

  try {
    // Example 1: Basic client creation with custom timeout
    console.log('1️⃣ Creating client with custom timeout configuration:');
    const client = await createClient({
      serverAddress: 'localhost:2206',
      timeout: {
        timeout: 15000,  // 15 seconds
        retries: 2,      // retry twice
        retryDelay: 1000 // 1 second between retries
      }
    });
    console.log('✅ Client created successfully\n');

    // Example 2: Using preset configurations
    console.log('2️⃣ Using preset configurations:');
    const devClient = await ClientPresets.development();
    console.log('✅ Development client created (60s timeout, 1 retry)');
    
    const prodClient = await ClientPresets.production('aggregator.avaprotocol.org:2206');
    console.log('✅ Production client created (30s timeout, 3 retries)');
    
    const testClient = await ClientPresets.testing();
    console.log('✅ Testing client created (5s timeout, no retries)\n');

    // Example 3: Different request types with timeouts
    console.log('3️⃣ Using different timeout strategies:');
    
    // Fast request for quick operations
    console.log('Attempting fast request (5s timeout)...');
    try {
      const fastResult = await client.sendFastRequest('GetKey', {
        owner: '0x123...',
        expired_at: Math.floor(Date.now() / 1000) + 3600,
        signature: '0xabc...'
      });
      console.log('✅ Fast request completed');
    } catch (error) {
      if (error.isTimeout) {
        console.log(`⏰ Fast request timed out after ${error.attemptsMade} attempts`);
      } else {
        console.log(`❌ Fast request failed: ${error.message}`);
      }
    }

    // Normal request with default settings
    console.log('Attempting normal request (30s timeout)...');
    try {
      const normalResult = await client.sendGrpcRequest('ListTasks', {
        smart_wallet_address: ['0x456...'],
        cursor: '',
        item_per_page: 10
      });
      console.log('✅ Normal request completed');
    } catch (error) {
      if (error.isTimeout) {
        console.log(`⏰ Normal request timed out after ${error.attemptsMade} attempts`);
      } else {
        console.log(`❌ Normal request failed: ${error.message}`);
      }
    }

    // Slow request for heavy operations
    console.log('Attempting slow request (2min timeout)...');
    try {
      const slowResult = await client.sendSlowRequest('RunNodeWithInputs', {
        node_type: 'customCode',
        node_config: {
          lang: { kind: 'numberValue', numberValue: 0 },
          source: { kind: 'stringValue', stringValue: 'console.log("Hello World"); 42;' }
        },
        input_variables: {
          testVar: { kind: 'stringValue', stringValue: 'test' }
        }
      });
      console.log('✅ Slow request completed');
    } catch (error) {
      if (error.isTimeout) {
        console.log(`⏰ Slow request timed out after ${error.attemptsMade} attempts`);
      } else {
        console.log(`❌ Slow request failed: ${error.message}`);
      }
    }

    // Example 4: Custom timeout for specific requests
    console.log('\n4️⃣ Custom timeout for specific request:');
    try {
      const customResult = await client.sendGrpcRequest('RunNodeWithInputs', {
        node_type: 'blockTrigger',
        node_config: {
          interval: { kind: 'numberValue', numberValue: 5 }
        },
        input_variables: {
          currentBlock: { kind: 'numberValue', numberValue: 12345 }
        }
      }, {}, {
        timeout: 10000,   // 10 seconds
        retries: 1,       // only 1 retry
        retryDelay: 500   // 500ms between retries
      });
      console.log('✅ Custom timeout request completed');
    } catch (error) {
      if (error.isTimeout) {
        console.log(`⏰ Custom request timed out after ${error.attemptsMade} attempts`);
      } else {
        console.log(`❌ Custom request failed: ${error.message}`);
      }
    }

    // Example 5: No retry request
    console.log('\n5️⃣ No retry request (fails immediately):');
    try {
      const noRetryResult = await client.sendNoRetryRequest('GetTask', {
        id: 'non-existent-task-id'
      });
      console.log('✅ No retry request completed');
    } catch (error) {
      console.log(`❌ No retry request failed immediately: ${error.message}`);
    }

    // Clean up
    client.close();
    devClient.close();
    prodClient.close();
    testClient.close();

    console.log('\n🎉 All examples completed successfully!');

  } catch (error) {
    console.error('❌ Example failed:', error.message);
  }
}

/**
 * Example of error handling patterns
 */
async function demonstrateErrorHandling() {
  console.log('\n🛡️ Error Handling Examples:\n');

  const client = await createClient({
    serverAddress: 'localhost:2206',
    timeout: { timeout: 5000, retries: 2, retryDelay: 1000 }
  });

  // Example 1: Timeout error with context
  console.log('1️⃣ Handling timeout errors:');
  try {
    await client.sendGrpcRequest('SlowMethod', {});
  } catch (error) {
    if (error.isTimeout) {
      console.log(`⏰ Timeout: ${error.message}`);
      console.log(`📊 Attempts made: ${error.attemptsMade}`);
      console.log(`🔧 Method: ${error.methodName}`);
    }
  }

  // Example 2: Retry-able vs non-retry-able errors  
  console.log('\n2️⃣ Different error types:');
  console.log(`
Retry-able errors (will be retried):
- Timeout errors
- UNAVAILABLE (14)
- DEADLINE_EXCEEDED (4) 
- RESOURCE_EXHAUSTED (8)

Non-retry-able errors (fail immediately):
- UNAUTHENTICATED (16)
- PERMISSION_DENIED (7)
- INVALID_ARGUMENT (3)
- NOT_FOUND (5)
  `);

  client.close();
}

// Run examples if this file is executed directly
if (require.main === module) {
  (async () => {
    await demonstrateTimeoutUsage();
    await demonstrateErrorHandling();
  })().catch(console.error);
}

module.exports = {
  demonstrateTimeoutUsage,
  demonstrateErrorHandling
};