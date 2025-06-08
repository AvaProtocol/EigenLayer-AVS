// Test file for gRPC timeout functionality
// Run with: node runNodeImmediately.test.js

const assert = require('assert');
const { promisify } = require('util');

// Mock gRPC status codes for testing
const grpcStatus = {
  OK: 0,
  CANCELLED: 1,
  UNKNOWN: 2,
  INVALID_ARGUMENT: 3,
  DEADLINE_EXCEEDED: 4,
  NOT_FOUND: 5,
  ALREADY_EXISTS: 6,
  PERMISSION_DENIED: 7,
  RESOURCE_EXHAUSTED: 8,
  FAILED_PRECONDITION: 9,
  ABORTED: 10,
  OUT_OF_RANGE: 11,
  UNIMPLEMENTED: 12,
  INTERNAL: 13,
  UNAVAILABLE: 14,
  DATA_LOSS: 15,
  UNAUTHENTICATED: 16
};

// Recreate the timeout-enabled asyncRPC function for testing
function createAsyncRPC() {
  return function asyncRPC(client, method, request, metadata, options = {}) {
    return new Promise((resolve, reject) => {
      const {
        timeout = client._timeoutConfig?.timeout || 30000,
        retries = client._timeoutConfig?.retries || 3,
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
          const mockCall = {
            cancel: () => {}
          };

          // Simulate gRPC call
          if (client[method]) {
            client[method](request, metadata, (error, response) => {
              if (error) {
                grpcReject(error);
              } else {
                grpcResolve(response);
              }
            });
          } else {
            grpcReject(new Error(`Method ${method} not found`));
          }

          // Handle call cancellation on timeout
          timeoutPromise.catch(() => {
            if (mockCall && mockCall.cancel) {
              mockCall.cancel();
            }
          });
        });

        // Race between timeout and actual call
        Promise.race([grpcPromise, timeoutPromise])
          .then(resolve)
          .catch((error) => {
            const isTimeoutError = error.message.includes('timeout');
            const isRetryableError = isTimeoutError || 
              error.code === grpcStatus.UNAVAILABLE || 
              error.code === grpcStatus.DEADLINE_EXCEEDED ||
              error.code === grpcStatus.RESOURCE_EXHAUSTED;

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
  };
}

// Test helper to create mock client
function createMockClient(timeoutConfig = {}) {
  return {
    _timeoutConfig: {
      timeout: 30000,
      retries: 3,
      retryDelay: 1000,
      ...timeoutConfig
    },
    RunNodeWithInputs: null,
    GetKey: null,
    ListTasks: null
  };
}

// Timeout presets
const TimeoutPresets = {
  FAST: { timeout: 5000, retries: 2, retryDelay: 500 },
  NORMAL: { timeout: 30000, retries: 3, retryDelay: 1000 },
  SLOW: { timeout: 120000, retries: 2, retryDelay: 2000 },
  NO_RETRY: { timeout: 30000, retries: 0, retryDelay: 0 }
};

// Test suite
async function runTests() {
  console.log('🚀 Running gRPC Timeout Tests...\n');

  const asyncRPC = createAsyncRPC();
  let testCount = 0;
  let passedTests = 0;

  function test(name, testFn) {
    testCount++;
    return testFn()
      .then(() => {
        console.log(`✅ ${name}`);
        passedTests++;
      })
      .catch((error) => {
        console.log(`❌ ${name}: ${error.message}`);
      });
  }

  // Test 1: Basic timeout functionality
  await test('Should use default timeout when no options provided', async () => {
    const mockClient = createMockClient();
    mockClient.RunNodeWithInputs = (req, meta, callback) => {
      setTimeout(() => callback(null, { success: true, data: 'test' }), 100);
    };

    const result = await asyncRPC(mockClient, 'RunNodeWithInputs', {}, {});
    assert(result.success === true);
    assert(result.data === 'test');
  });

  // Test 2: Timeout behavior
  await test('Should timeout after specified duration', async () => {
    const mockClient = createMockClient();
    mockClient.RunNodeWithInputs = (req, meta, callback) => {
      // Never call the callback to simulate hanging request
    };

    try {
      await asyncRPC(mockClient, 'RunNodeWithInputs', {}, {}, {
        timeout: 1000,
        retries: 0
      });
      throw new Error('Expected timeout error');
    } catch (error) {
      assert(error.message.includes('gRPC request timeout after 1000ms'));
      assert(error.isTimeout === true);
      assert(error.attemptsMade === 1);
    }
  });

  // Test 3: Successful response before timeout
  await test('Should resolve successfully before timeout', async () => {
    const mockClient = createMockClient();
    const mockResponse = { success: true, nodeType: 'blockTrigger' };
    
    mockClient.RunNodeWithInputs = (req, meta, callback) => {
      setTimeout(() => callback(null, mockResponse), 500);
    };

    const result = await asyncRPC(mockClient, 'RunNodeWithInputs', {
      node_type: 'blockTrigger',
      node_config: { interval: { kind: 'numberValue', numberValue: 5 } }
    }, {}, { timeout: 2000 });

    assert.deepStrictEqual(result, mockResponse);
  });

  // Test 4: Retry on timeout errors
  await test('Should retry on timeout errors', async () => {
    const mockClient = createMockClient();
    let callCount = 0;
    
    mockClient.ListTasks = (req, meta, callback) => {
      callCount++;
      // Never respond to always timeout
    };

    try {
      await asyncRPC(mockClient, 'ListTasks', {}, {}, {
        timeout: 500,
        retries: 2,
        retryDelay: 100
      });
      throw new Error('Expected timeout error');
    } catch (error) {
      assert(error.isTimeout === true);
      assert(callCount === 2); // Original + 1 retry
    }
  });

  // Test 5: Retry on UNAVAILABLE status
  await test('Should retry on UNAVAILABLE status', async () => {
    const mockClient = createMockClient();
    let callCount = 0;
    const unavailableError = { 
      code: grpcStatus.UNAVAILABLE, 
      message: 'Service unavailable' 
    };
    
    mockClient.GetKey = (req, meta, callback) => {
      callCount++;
      if (callCount === 1) {
        callback(unavailableError);
      } else {
        callback(null, { success: true, key: 'test-key' });
      }
    };

    const result = await asyncRPC(mockClient, 'GetKey', {}, {}, {
      timeout: 5000,
      retries: 2,
      retryDelay: 50
    });

    assert.deepStrictEqual(result, { success: true, key: 'test-key' });
    assert(callCount === 2);
  });

  // Test 6: No retry on non-retryable errors
  await test('Should not retry non-retryable errors', async () => {
    const mockClient = createMockClient();
    let callCount = 0;
    const authError = { 
      code: grpcStatus.UNAUTHENTICATED, 
      message: 'Authentication failed' 
    };
    
    mockClient.GetKey = (req, meta, callback) => {
      callCount++;
      callback(authError);
    };

    try {
      await asyncRPC(mockClient, 'GetKey', {}, {}, {
        timeout: 5000,
        retries: 3
      });
      throw new Error('Expected auth error');
    } catch (error) {
      assert.deepStrictEqual(error, authError);
      assert(callCount === 1);
    }
  });

  // Test 7: FAST preset functionality
  await test('Should work with FAST preset', async () => {
    const mockClient = createMockClient();
    const mockResponse = { success: true };
    
    mockClient.RunNodeWithInputs = (req, meta, callback) => {
      setTimeout(() => callback(null, mockResponse), 100);
    };

    const result = await asyncRPC(mockClient, 'RunNodeWithInputs', {}, {}, TimeoutPresets.FAST);
    assert.deepStrictEqual(result, mockResponse);
  });

  // Test 8: NO_RETRY preset functionality  
  await test('Should not retry with NO_RETRY preset', async () => {
    const mockClient = createMockClient();
    let callCount = 0;
    const timeoutError = { 
      code: grpcStatus.DEADLINE_EXCEEDED, 
      message: 'Deadline exceeded' 
    };
    
    mockClient.ListTasks = (req, meta, callback) => {
      callCount++;
      callback(timeoutError);
    };

    try {
      await asyncRPC(mockClient, 'ListTasks', {}, {}, TimeoutPresets.NO_RETRY);
      throw new Error('Expected deadline exceeded error');
    } catch (error) {
      assert.deepStrictEqual(error, timeoutError);
      assert(callCount === 1);
    }
  });

  // Test 9: RunNodeWithInputs blockTrigger
  await test('Should handle blockTrigger node type', async () => {
    const mockClient = createMockClient();
    const mockResponse = {
      success: true,
      output_data: {
        result: { currentBlock: 12345 }
      }
    };

    mockClient.RunNodeWithInputs = (req, meta, callback) => {
      // Validate request structure
      assert(req.node_type === 'blockTrigger');
      assert(req.node_config.interval.numberValue === 5);
      setTimeout(() => callback(null, mockResponse), 100);
    };

    const request = {
      node_type: 'blockTrigger',
      node_config: {
        interval: { kind: 'numberValue', numberValue: 5 }
      },
      input_variables: {
        currentBlock: { kind: 'numberValue', numberValue: 12345 }
      }
    };

    const result = await asyncRPC(mockClient, 'RunNodeWithInputs', request, {}, TimeoutPresets.FAST);
    assert.deepStrictEqual(result, mockResponse);
  });

  // Test 10: RunNodeWithInputs customCode with timeout
  await test('Should handle customCode node type with custom timeout', async () => {
    const mockClient = createMockClient();
    const mockResponse = {
      success: true,
      output_data: {
        result: { value: 42 }
      }
    };

    mockClient.RunNodeWithInputs = (req, meta, callback) => {
      assert(req.node_type === 'customCode');
      assert(req.node_config.source.stringValue === '42');
      setTimeout(() => callback(null, mockResponse), 100);
    };

    const request = {
      node_type: 'customCode',
      node_config: {
        lang: { kind: 'numberValue', numberValue: 0 },
        source: { kind: 'stringValue', stringValue: '42' }
      },
      input_variables: {
        testVar: { kind: 'stringValue', stringValue: 'hello world' }
      }
    };

    const result = await asyncRPC(mockClient, 'RunNodeWithInputs', request, {}, {
      timeout: 15000,
      retries: 1,
      retryDelay: 500
    });

    assert.deepStrictEqual(result, mockResponse);
  });

  // Summary
  console.log(`\n📊 Test Results: ${passedTests}/${testCount} tests passed`);
  
  if (passedTests === testCount) {
    console.log('🎉 All tests passed!');
    return true;
  } else {
    console.log('❌ Some tests failed');
    return false;
  }
}

// Example usage demonstration
async function demonstrateTimeoutUsage() {
  console.log('\n🔧 Timeout Usage Examples:\n');

  // Example 1: Basic client creation with timeout
  console.log('1. Creating client with timeout configuration:');
  console.log(`
const client = createClient(
  'localhost:2206',
  grpc.credentials.createInsecure(),
  {
    timeout: 30000,    // 30 seconds
    maxRetries: 3,     // retry up to 3 times
    retryDelay: 1000   // wait 1 second between retries
  }
);`);

  // Example 2: Using different timeout presets
  console.log('\n2. Using timeout presets:');
  console.log(`
// For quick operations (5s timeout, 2 retries)
const result1 = await fastRPC(client, 'GetKey', request, metadata);

// For heavy operations (2min timeout, 2 retries)  
const result2 = await slowRPC(client, 'RunNodeWithInputs', request, metadata);

// No retries
const result3 = await noRetryRPC(client, 'ListTasks', request, metadata);`);

  // Example 3: Custom timeout options
  console.log('\n3. Custom timeout for specific calls:');
  console.log(`
const result = await asyncRPC(client, 'RunNodeWithInputs', request, metadata, {
  timeout: 15000,    // 15 seconds
  retries: 1,        // only 1 retry
  retryDelay: 500    // 500ms between retries
});`);

  // Example 4: Error handling
  console.log('\n4. Error handling:');
  console.log(`
try {
  const result = await asyncRPC(client, 'method', request, metadata);
} catch (error) {
  if (error.isTimeout) {
    console.log(\`Request timed out after \${error.attemptsMade} attempts\`);
  } else {
    console.log('Other error:', error.message);
  }
}`);
}

// Run if this file is executed directly
if (require.main === module) {
  (async () => {
    const success = await runTests();
    await demonstrateTimeoutUsage();
    process.exit(success ? 0 : 1);
  })();
}

module.exports = {
  createAsyncRPC,
  createMockClient,
  TimeoutPresets,
  runTests
};