// Jest setup file for Ava Protocol SDK tests

// Setup environment variables for testing
if (process.env.TEST_ENV === 'dev') {
  process.env.NODE_ENV = 'development';
}

// Global test utilities
global.testUtils = {
  // Mock gRPC metadata
  createMockMetadata: () => ({
    add: jest.fn(),
    set: jest.fn(),
    get: jest.fn(),
    getMap: jest.fn(() => ({}))
  }),

  // Common timeout values for testing
  timeouts: {
    fast: 1000,
    normal: 5000,
    slow: 10000
  }
};

// Console warning suppression for known issues during testing
const originalWarn = console.warn;
console.warn = (...args) => {
  // Suppress specific warnings during tests
  if (args[0] && typeof args[0] === 'string') {
    if (args[0].includes('deprecated') || args[0].includes('not supported')) {
      return;
    }
  }
  originalWarn.apply(console, args);
};