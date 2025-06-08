const { AvaProtocolClient, TimeoutPresets } = require('./client');

/**
 * Create and initialize an Ava Protocol client
 * @param {Object} config - Client configuration
 * @param {string} config.serverAddress - gRPC server address
 * @param {Object} [config.credentials] - gRPC credentials
 * @param {Object} [config.timeout] - Default timeout configuration
 * @returns {Promise<AvaProtocolClient>} Initialized client
 */
async function createClient(config) {
  const client = new AvaProtocolClient(config);
  await client.initialize();
  return client;
}

/**
 * Create client with common configurations
 */
const ClientPresets = {
  /**
   * Development client with relaxed timeouts
   */
  development: (serverAddress = 'localhost:2206') => createClient({
    serverAddress,
    timeout: {
      timeout: 60000,  // 1 minute for development
      retries: 1,
      retryDelay: 1000
    }
  }),

  /**
   * Production client with strict timeouts
   */
  production: (serverAddress) => createClient({
    serverAddress,
    timeout: {
      timeout: 30000,  // 30 seconds for production
      retries: 3,
      retryDelay: 2000
    }
  }),

  /**
   * Testing client with fast timeouts
   */
  testing: (serverAddress = 'localhost:2206') => createClient({
    serverAddress,
    timeout: {
      timeout: 5000,   // 5 seconds for testing
      retries: 0,
      retryDelay: 0
    }
  })
};

module.exports = {
  AvaProtocolClient,
  TimeoutPresets,
  createClient,
  ClientPresets
};