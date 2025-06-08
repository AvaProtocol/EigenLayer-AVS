module.exports = {
  testEnvironment: 'node',
  testMatch: [
    '**/__tests__/**/*.test.js',
    '**/?(*.)+(spec|test).js'
  ],
  collectCoverageFrom: [
    'packages/**/*.js',
    '!packages/**/node_modules/**',
    '!packages/**/examples/**',
    '!**/node_modules/**'
  ],
  coverageDirectory: 'coverage',
  verbose: true,
  testTimeout: 10000,
  setupFiles: ['<rootDir>/jest.setup.js']
};