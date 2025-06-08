const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");

/**
 * Timeout configuration options
 * @typedef {Object} TimeoutConfig
 * @property {number} [timeout=30000] - Request timeout in milliseconds
 * @property {number} [retries=3] - Maximum number of retry attempts
 * @property {number} [retryDelay=1000] - Delay between retries in milliseconds
 */

/**
 * Client configuration options
 * @typedef {Object} ClientConfig
 * @property {string} serverAddress - gRPC server address
 * @property {Object} [credentials] - gRPC credentials
 * @property {TimeoutConfig} [timeout] - Default timeout configuration
 */

/**
 * Predefined timeout presets for common use cases
 */
const TimeoutPresets = {
  FAST: { timeout: 5000, retries: 2, retryDelay: 500 },     // 5s for quick operations
  NORMAL: { timeout: 30000, retries: 3, retryDelay: 1000 }, // 30s for normal operations  
  SLOW: { timeout: 120000, retries: 2, retryDelay: 2000 },  // 2min for heavy operations
  NO_RETRY: { timeout: 30000, retries: 0, retryDelay: 0 }   // No retries
};

/**
 * Enhanced gRPC client with timeout and retry functionality
 */
class AvaProtocolClient {
  constructor(config) {
    this.config = this._validateConfig(config);
    this.client = null;
    this._isInitialized = false;
  }

  /**
   * Initialize the gRPC client
   */
  async initialize() {
    if (this._isInitialized) {
      return;
    }

    try {
      // Load the protobuf definition - adjust path as needed
      const packageDefinition = protoLoader.loadSync("../../protobuf/avs.proto", {
        keepCase: true,
        longs: String,
        enums: String,
        defaults: true,
        oneofs: true,
      });

      const protoDescriptor = grpc.loadPackageDefinition(packageDefinition);
      const apProto = protoDescriptor.aggregator;
      
      // Create the client with configured credentials
      this.client = new apProto.Aggregator(
        this.config.serverAddress,
        this.config.credentials || grpc.credentials.createInsecure()
      );

      // Store timeout configuration on client for later use
      this.client._timeoutConfig = this.config.timeout;
      
      this._isInitialized = true;
    } catch (error) {
      throw new Error(`Failed to initialize gRPC client: ${error.message}`);
    }
  }

  /**
   * Send a gRPC request with timeout and retry support
   * @param {string} method - gRPC method name
   * @param {Object} request - Request object
   * @param {Object} [metadata] - gRPC metadata
   * @param {TimeoutConfig} [options] - Timeout options for this specific request
   * @returns {Promise<Object>} Response object
   */
  async sendGrpcRequest(method, request, metadata = {}, options = {}) {
    if (!this._isInitialized) {
      throw new Error('Client not initialized. Call initialize() first.');
    }

    return this._executeRequest(method, request, metadata, options);
  }

  /**
   * Convenience method for quick operations
   */
  async sendFastRequest(method, request, metadata = {}) {
    return this.sendGrpcRequest(method, request, metadata, TimeoutPresets.FAST);
  }

  /**
   * Convenience method for heavy operations
   */
  async sendSlowRequest(method, request, metadata = {}) {
    return this.sendGrpcRequest(method, request, metadata, TimeoutPresets.SLOW);
  }

  /**
   * Convenience method for operations without retries
   */
  async sendNoRetryRequest(method, request, metadata = {}) {
    return this.sendGrpcRequest(method, request, metadata, TimeoutPresets.NO_RETRY);
  }

  /**
   * Execute gRPC request with timeout and retry logic
   * @private
   */
  _executeRequest(method, request, metadata, options = {}) {
    return new Promise((resolve, reject) => {
      const {
        timeout = this.client._timeoutConfig?.timeout || 30000,
        retries = this.client._timeoutConfig?.retries || 3,
        retryDelay = this.client._timeoutConfig?.retryDelay || 1000
      } = options;

      let attempt = 0;
      
      const executeRequest = () => {
        attempt++;
        
        // Create a timeout promise
        const timeoutPromise = new Promise((_, timeoutReject) => {
          setTimeout(() => {
            timeoutReject(new Error(`gRPC request timeout after ${timeout}ms for method ${method}`));
          }, timeout);
        });

        // Create the actual gRPC call promise
        const grpcPromise = new Promise((grpcResolve, grpcReject) => {
          if (!this.client[method]) {
            grpcReject(new Error(`Method ${method} not found on client`));
            return;
          }

          const call = this.client[method].bind(this.client)(request, metadata, (error, response) => {
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
      };

      executeRequest();
    });
  }

  /**
   * Validate client configuration
   * @private
   */
  _validateConfig(config) {
    if (!config || typeof config !== 'object') {
      throw new Error('Client configuration is required');
    }

    if (!config.serverAddress || typeof config.serverAddress !== 'string') {
      throw new Error('serverAddress is required and must be a string');
    }

    // Set default timeout configuration
    const defaultTimeout = {
      timeout: 30000,
      retries: 3,
      retryDelay: 1000
    };

    return {
      ...config,
      timeout: { ...defaultTimeout, ...config.timeout }
    };
  }

  /**
   * Close the gRPC client connection
   */
  close() {
    if (this.client && this.client.close) {
      this.client.close();
    }
    this._isInitialized = false;
  }

  /**
   * Check if client is initialized
   */
  get isInitialized() {
    return this._isInitialized;
  }
}

module.exports = {
  AvaProtocolClient,
  TimeoutPresets
};