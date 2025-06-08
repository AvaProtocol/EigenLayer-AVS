// Timeout functionality test for Ava Protocol SDK
const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");

// Timeout presets
const TimeoutPresets = {
  FAST: { timeout: 5000, retries: 2, retryDelay: 500 },
  NORMAL: { timeout: 30000, retries: 3, retryDelay: 1000 },
  SLOW: { timeout: 120000, retries: 2, retryDelay: 2000 },
  NO_RETRY: { timeout: 30000, retries: 0, retryDelay: 0 }
};

// Enhanced asyncRPC function with timeout support (from ../example.js)
function createAsyncRPCWithTimeout() {
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
                error.methodName = method;
              }
              reject(error);
            }
          });
      }

      executeRequest();
    });
  };
}

// Mock client for testing
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

describe('Ava Protocol SDK - Timeout Functionality', () => {
  let asyncRPC;

  beforeEach(() => {
    asyncRPC = createAsyncRPCWithTimeout();
  });

  describe('Basic Timeout Functionality', () => {
    it('should execute successful request within timeout', async () => {
      const mockClient = createMockClient();
      const mockResponse = { success: true, data: 'test' };
      
      mockClient.RunNodeWithInputs = jest.fn((req, meta, callback) => {
        setTimeout(() => callback(null, mockResponse), 100);
      });

      const result = await asyncRPC(mockClient, 'RunNodeWithInputs', {
        node_type: 'blockTrigger'
      }, {});

      expect(result).toEqual(mockResponse);
      expect(mockClient.RunNodeWithInputs).toHaveBeenCalledTimes(1);
    });

    it('should timeout after specified duration', async () => {
      const mockClient = createMockClient();
      
      mockClient.RunNodeWithInputs = jest.fn((req, meta, callback) => {
        // Never call callback to simulate hanging request
      });

      await expect(asyncRPC(mockClient, 'RunNodeWithInputs', {}, {}, {
        timeout: 1000,
        retries: 0
      })).rejects.toMatchObject({
        message: expect.stringContaining('gRPC request timeout after 1000ms'),
        isTimeout: true,
        attemptsMade: 1,
        methodName: 'RunNodeWithInputs'
      });
    });

    it('should retry on timeout errors', async () => {
      const mockClient = createMockClient();
      let callCount = 0;
      
      mockClient.ListTasks = jest.fn((req, meta, callback) => {
        callCount++;
        // Never respond to always timeout
      });

      await expect(asyncRPC(mockClient, 'ListTasks', {}, {}, {
        timeout: 500,
        retries: 2,
        retryDelay: 100
      })).rejects.toMatchObject({
        isTimeout: true,
        attemptsMade: 2
      });

      expect(callCount).toBe(2);
    });

    it('should retry on UNAVAILABLE status', async () => {
      const mockClient = createMockClient();
      let callCount = 0;
      const unavailableError = { 
        code: grpc.status.UNAVAILABLE, 
        message: 'Service unavailable' 
      };
      
      mockClient.GetKey = jest.fn((req, meta, callback) => {
        callCount++;
        if (callCount === 1) {
          callback(unavailableError);
        } else {
          callback(null, { success: true, key: 'test-key' });
        }
      });

      const result = await asyncRPC(mockClient, 'GetKey', {}, {}, {
        timeout: 5000,
        retries: 2,
        retryDelay: 50
      });

      expect(result).toEqual({ success: true, key: 'test-key' });
      expect(callCount).toBe(2);
    });
  });

  describe('Timeout Presets', () => {
    it('should work with FAST preset', async () => {
      const mockClient = createMockClient();
      const mockResponse = { success: true };
      
      mockClient.RunNodeWithInputs = jest.fn((req, meta, callback) => {
        setTimeout(() => callback(null, mockResponse), 100);
      });

      const result = await asyncRPC(mockClient, 'RunNodeWithInputs', {}, {}, TimeoutPresets.FAST);
      expect(result).toEqual(mockResponse);
    });

    it('should not retry with NO_RETRY preset', async () => {
      const mockClient = createMockClient();
      let callCount = 0;
      const timeoutError = { 
        code: grpc.status.DEADLINE_EXCEEDED, 
        message: 'Deadline exceeded' 
      };
      
      mockClient.ListTasks = jest.fn((req, meta, callback) => {
        callCount++;
        callback(timeoutError);
      });

      await expect(asyncRPC(mockClient, 'ListTasks', {}, {}, TimeoutPresets.NO_RETRY))
        .rejects.toEqual(timeoutError);

      expect(callCount).toBe(1);
    });
  });

  describe('RunNodeWithInputs Specific Tests', () => {
    it('should handle blockTrigger node with fast timeout', async () => {
      const mockClient = createMockClient();
      const mockResponse = {
        success: true,
        output_data: {
          result: { currentBlock: 12345 }
        }
      };

      mockClient.RunNodeWithInputs = jest.fn((req, meta, callback) => {
        expect(req.node_type).toBe('blockTrigger');
        expect(req.node_config.interval.numberValue).toBe(5);
        setTimeout(() => callback(null, mockResponse), 100);
      });

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
      expect(result).toEqual(mockResponse);
    });

    it('should handle customCode node with custom timeout', async () => {
      const mockClient = createMockClient();
      const mockResponse = {
        success: true,
        output_data: {
          result: { value: 42 }
        }
      };

      mockClient.RunNodeWithInputs = jest.fn((req, meta, callback) => {
        expect(req.node_type).toBe('customCode');
        expect(req.node_config.source.stringValue).toBe('42');
        setTimeout(() => callback(null, mockResponse), 100);
      });

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

      expect(result).toEqual(mockResponse);
    });
  });

  describe('Error Handling', () => {
    it('should not retry non-retryable errors', async () => {
      const mockClient = createMockClient();
      let callCount = 0;
      const authError = { 
        code: grpc.status.UNAUTHENTICATED, 
        message: 'Authentication failed' 
      };
      
      mockClient.GetKey = jest.fn((req, meta, callback) => {
        callCount++;
        callback(authError);
      });

      await expect(asyncRPC(mockClient, 'GetKey', {}, {}, {
        timeout: 5000,
        retries: 3
      })).rejects.toEqual(authError);

      expect(callCount).toBe(1);
    });

    it('should add timeout context to timeout errors', async () => {
      const mockClient = createMockClient();
      
      mockClient.GetKey = jest.fn((req, meta, callback) => {
        // Never respond
      });

      await expect(asyncRPC(mockClient, 'GetKey', {}, {}, {
        timeout: 1000,
        retries: 1,
        retryDelay: 100
      })).rejects.toMatchObject({
        isTimeout: true,
        attemptsMade: 1,
        methodName: 'GetKey',
        message: expect.stringContaining('timeout after 1000ms')
      });
    });
  });
});