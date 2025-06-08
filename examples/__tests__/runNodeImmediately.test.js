const { AvaProtocolClient, TimeoutPresets } = require('../../packages/sdk-js/src/client');

// Mock the gRPC dependencies
jest.mock('@grpc/grpc-js', () => ({
  credentials: {
    createInsecure: jest.fn(() => ({}))
  },
  status: {
    OK: 0,
    UNAVAILABLE: 14,
    DEADLINE_EXCEEDED: 4,
    RESOURCE_EXHAUSTED: 8,
    UNAUTHENTICATED: 16
  },
  loadPackageDefinition: jest.fn()
}));

jest.mock('@grpc/proto-loader', () => ({
  loadSync: jest.fn()
}));

describe('AvaProtocolClient Timeout Functionality', () => {
  let mockAggregator;
  let mockProtoDescriptor;
  let grpc;
  let protoLoader;

  beforeEach(() => {
    jest.clearAllMocks();
    
    // Setup mocks
    grpc = require('@grpc/grpc-js');
    protoLoader = require('@grpc/proto-loader');
    
    // Mock Aggregator class
    mockAggregator = jest.fn().mockImplementation(function(address, credentials) {
      this.address = address;
      this.credentials = credentials;
      this._timeoutConfig = null;
      
      // Mock methods
      this.RunNodeWithInputs = jest.fn();
      this.GetKey = jest.fn();
      this.ListTasks = jest.fn();
      this.close = jest.fn();
    });
    
    mockProtoDescriptor = {
      aggregator: {
        Aggregator: mockAggregator
      }
    };
    
    grpc.loadPackageDefinition.mockReturnValue(mockProtoDescriptor);
    protoLoader.loadSync.mockReturnValue({});
  });

  describe('Client Initialization', () => {
    it('should create client with default timeout configuration', () => {
      const client = new AvaProtocolClient({
        serverAddress: 'localhost:2206'
      });

      expect(client.config.serverAddress).toBe('localhost:2206');
      expect(client.config.timeout.timeout).toBe(30000);
      expect(client.config.timeout.retries).toBe(3);
      expect(client.config.timeout.retryDelay).toBe(1000);
    });

    it('should create client with custom timeout configuration', () => {
      const client = new AvaProtocolClient({
        serverAddress: 'localhost:2206',
        timeout: {
          timeout: 15000,
          retries: 2,
          retryDelay: 500
        }
      });

      expect(client.config.timeout.timeout).toBe(15000);
      expect(client.config.timeout.retries).toBe(2);
      expect(client.config.timeout.retryDelay).toBe(500);
    });

    it('should throw error for missing serverAddress', () => {
      expect(() => {
        new AvaProtocolClient({});
      }).toThrow('serverAddress is required');
    });

    it('should initialize gRPC client correctly', async () => {
      const client = new AvaProtocolClient({
        serverAddress: 'localhost:2206'
      });

      await client.initialize();

      expect(client.isInitialized).toBe(true);
      expect(protoLoader.loadSync).toHaveBeenCalled();
      expect(grpc.loadPackageDefinition).toHaveBeenCalled();
      expect(mockAggregator).toHaveBeenCalledWith('localhost:2206', {});
    });
  });

  describe('Request Execution with Timeouts', () => {
    let client;

    beforeEach(async () => {
      client = new AvaProtocolClient({
        serverAddress: 'localhost:2206',
        timeout: { timeout: 5000, retries: 2, retryDelay: 100 }
      });
      await client.initialize();
    });

    it('should execute successful request', async () => {
      const mockResponse = { success: true, data: 'test' };
      const mockMethod = jest.fn((req, meta, callback) => {
        setTimeout(() => callback(null, mockResponse), 50);
      });
      
      client.client.RunNodeWithInputs = mockMethod;

      const result = await client.sendGrpcRequest('RunNodeWithInputs', {
        node_type: 'blockTrigger'
      });

      expect(result).toEqual(mockResponse);
      expect(mockMethod).toHaveBeenCalledTimes(1);
    });

    it('should timeout after specified duration', async () => {
      jest.useFakeTimers();
      
      const mockMethod = jest.fn((req, meta, callback) => {
        // Never call callback to simulate hanging request
      });
      
      client.client.RunNodeWithInputs = mockMethod;

      const promise = client.sendGrpcRequest('RunNodeWithInputs', {}, {}, {
        timeout: 1000,
        retries: 0
      });

      jest.advanceTimersByTime(1001);

      await expect(promise).rejects.toMatchObject({
        message: expect.stringContaining('gRPC request timeout after 1000ms'),
        isTimeout: true,
        attemptsMade: 1,
        methodName: 'RunNodeWithInputs'
      });

      jest.useRealTimers();
    });

    it('should retry on timeout errors', async () => {
      jest.useFakeTimers();
      
      let callCount = 0;
      const mockMethod = jest.fn((req, meta, callback) => {
        callCount++;
        // Never respond to always timeout
      });
      
      client.client.ListTasks = mockMethod;

      const promise = client.sendGrpcRequest('ListTasks', {}, {}, {
        timeout: 500,
        retries: 2,
        retryDelay: 100
      });

      // First timeout
      jest.advanceTimersByTime(501);
      // Retry delay
      jest.advanceTimersByTime(100);
      // Second timeout
      jest.advanceTimersByTime(501);

      await expect(promise).rejects.toMatchObject({
        isTimeout: true,
        attemptsMade: 2
      });

      expect(callCount).toBe(2);
      jest.useRealTimers();
    });

    it('should retry on UNAVAILABLE status', async () => {
      let callCount = 0;
      const unavailableError = { 
        code: 14, // grpc.status.UNAVAILABLE
        message: 'Service unavailable' 
      };
      
      const mockMethod = jest.fn((req, meta, callback) => {
        callCount++;
        if (callCount === 1) {
          callback(unavailableError);
        } else {
          callback(null, { success: true, key: 'test-key' });
        }
      });
      
      client.client.GetKey = mockMethod;

      const result = await client.sendGrpcRequest('GetKey', {}, {}, {
        timeout: 5000,
        retries: 2,
        retryDelay: 50
      });

      expect(result).toEqual({ success: true, key: 'test-key' });
      expect(callCount).toBe(2);
    });

    it('should not retry non-retryable errors', async () => {
      const authError = { 
        code: 16, // grpc.status.UNAUTHENTICATED
        message: 'Authentication failed' 
      };
      
      const mockMethod = jest.fn((req, meta, callback) => {
        callback(authError);
      });
      
      client.client.GetKey = mockMethod;

      await expect(client.sendGrpcRequest('GetKey', {}, {}, {
        timeout: 5000,
        retries: 3
      })).rejects.toEqual(authError);

      expect(mockMethod).toHaveBeenCalledTimes(1);
    });
  });

  describe('Convenience Methods', () => {
    let client;

    beforeEach(async () => {
      client = new AvaProtocolClient({
        serverAddress: 'localhost:2206'
      });
      await client.initialize();
    });

    it('should use FAST preset for sendFastRequest', async () => {
      const mockResponse = { success: true };
      const mockMethod = jest.fn((req, meta, callback) => {
        setTimeout(() => callback(null, mockResponse), 50);
      });
      
      client.client.RunNodeWithInputs = mockMethod;

      const result = await client.sendFastRequest('RunNodeWithInputs', {
        node_type: 'blockTrigger'
      });

      expect(result).toEqual(mockResponse);
    });

    it('should use SLOW preset for sendSlowRequest', async () => {
      const mockResponse = { success: true };
      const mockMethod = jest.fn((req, meta, callback) => {
        setTimeout(() => callback(null, mockResponse), 50);
      });
      
      client.client.RunNodeWithInputs = mockMethod;

      const result = await client.sendSlowRequest('RunNodeWithInputs', {
        node_type: 'customCode'
      });

      expect(result).toEqual(mockResponse);
    });

    it('should use NO_RETRY preset for sendNoRetryRequest', async () => {
      const timeoutError = { 
        code: 4, // grpc.status.DEADLINE_EXCEEDED
        message: 'Deadline exceeded' 
      };
      
      const mockMethod = jest.fn((req, meta, callback) => {
        callback(timeoutError);
      });
      
      client.client.ListTasks = mockMethod;

      await expect(client.sendNoRetryRequest('ListTasks', {}))
        .rejects.toEqual(timeoutError);

      expect(mockMethod).toHaveBeenCalledTimes(1);
    });
  });

  describe('RunNodeWithInputs Specific Tests', () => {
    let client;

    beforeEach(async () => {
      client = new AvaProtocolClient({
        serverAddress: 'localhost:2206'
      });
      await client.initialize();
    });

    it('should handle blockTrigger node with fast timeout', async () => {
      const mockResponse = {
        success: true,
        output_data: {
          result: { currentBlock: 12345 }
        }
      };

      const mockMethod = jest.fn((req, meta, callback) => {
        expect(req.node_type).toBe('blockTrigger');
        expect(req.node_config.interval.numberValue).toBe(5);
        setTimeout(() => callback(null, mockResponse), 100);
      });
      
      client.client.RunNodeWithInputs = mockMethod;

      const request = {
        node_type: 'blockTrigger',
        node_config: {
          interval: { kind: 'numberValue', numberValue: 5 }
        },
        input_variables: {
          currentBlock: { kind: 'numberValue', numberValue: 12345 }
        }
      };

      const result = await client.sendFastRequest('RunNodeWithInputs', request);
      expect(result).toEqual(mockResponse);
    });

    it('should handle customCode node with custom timeout', async () => {
      const mockResponse = {
        success: true,
        output_data: {
          result: { value: 42 }
        }
      };

      const mockMethod = jest.fn((req, meta, callback) => {
        expect(req.node_type).toBe('customCode');
        expect(req.node_config.source.stringValue).toBe('42');
        setTimeout(() => callback(null, mockResponse), 100);
      });
      
      client.client.RunNodeWithInputs = mockMethod;

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

      const result = await client.sendGrpcRequest('RunNodeWithInputs', request, {}, {
        timeout: 15000,
        retries: 1,
        retryDelay: 500
      });

      expect(result).toEqual(mockResponse);
    });

    it('should handle node execution failures gracefully', async () => {
      const mockResponse = {
        success: false,
        error: 'Node execution failed: Invalid syntax'
      };

      const mockMethod = jest.fn((req, meta, callback) => {
        callback(null, mockResponse);
      });
      
      client.client.RunNodeWithInputs = mockMethod;

      const request = {
        node_type: 'customCode',
        node_config: {
          lang: { kind: 'numberValue', numberValue: 0 },
          source: { kind: 'stringValue', stringValue: 'invalid syntax here' }
        }
      };

      const result = await client.sendFastRequest('RunNodeWithInputs', request);
      expect(result.success).toBe(false);
      expect(result.error).toContain('Node execution failed');
    });
  });

  describe('Error Handling', () => {
    let client;

    beforeEach(async () => {
      client = new AvaProtocolClient({
        serverAddress: 'localhost:2206'
      });
      await client.initialize();
    });

    it('should throw error when client not initialized', async () => {
      const uninitializedClient = new AvaProtocolClient({
        serverAddress: 'localhost:2206'
      });

      await expect(uninitializedClient.sendGrpcRequest('GetKey', {}))
        .rejects.toThrow('Client not initialized');
    });

    it('should throw error for non-existent method', async () => {
      await expect(client.sendGrpcRequest('NonExistentMethod', {}))
        .rejects.toThrow('Method NonExistentMethod not found on client');
    });

    it('should add timeout context to timeout errors', async () => {
      jest.useFakeTimers();
      
      const mockMethod = jest.fn((req, meta, callback) => {
        // Never respond
      });
      
      client.client.GetKey = mockMethod;

      const promise = client.sendGrpcRequest('GetKey', {}, {}, {
        timeout: 2000,
        retries: 1,
        retryDelay: 300
      });

      // First timeout
      jest.advanceTimersByTime(2001);
      // Retry delay
      jest.advanceTimersByTime(300);
      // Second timeout
      jest.advanceTimersByTime(2001);

      await expect(promise).rejects.toMatchObject({
        isTimeout: true,
        attemptsMade: 1,
        methodName: 'GetKey',
        message: expect.stringContaining('timeout after 2000ms')
      });

      jest.useRealTimers();
    });
  });

  describe('Client Lifecycle', () => {
    it('should close client properly', async () => {
      const client = new AvaProtocolClient({
        serverAddress: 'localhost:2206'
      });
      
      await client.initialize();
      expect(client.isInitialized).toBe(true);
      
      client.close();
      expect(client.isInitialized).toBe(false);
      expect(client.client.close).toHaveBeenCalled();
    });
  });
});