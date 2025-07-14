# GraphQL Package Testing

This package contains GraphQL client functionality with comprehensive testing coverage.

## Test Structure

### Unit Tests (`graphql_test.go`)

- **Purpose**: Tests the GraphQL client functionality without external dependencies
- **Approach**: Uses a mock HTTP server to simulate GraphQL responses
- **Run command**: `go test .` (runs by default in CI/CD)
- **Benefits**:
  - Fast execution
  - Reliable (no external dependencies)
  - Always available in CI/CD pipelines

### Integration Tests (`graphql_integration_test.go`)

- **Purpose**: Tests the GraphQL client against the real SpaceX API
- **Approach**: Makes actual HTTP requests to `https://spacex-production.up.railway.app/`
- **Run command**: `go test -tags=integration`
- **Benefits**:
  - Tests real-world API compatibility
  - Validates actual data formats and responses
  - Automatically skips if external API is unavailable

## CI/CD Behavior

The CI/CD pipeline now runs only the unit tests by default, ensuring:

- ✅ Fast and reliable test execution
- ✅ No failures due to external API downtime
- ✅ Consistent test results across environments

## Running Tests

```bash
# Run unit tests only (default)
go test .

# Run integration tests only
go test -tags=integration

# Run both unit and integration tests
go test . && go test -tags=integration

# Verbose output
go test . -v
go test -tags=integration -v
```

## Test Coverage

- **Unit Tests**: Cover all GraphQL client functionality with mocked responses
- **Integration Tests**: Validate against real SpaceX API with resilient error handling
- **Error Handling**: Both test types handle network failures gracefully

## Best Practices

1. **Unit tests should be the primary testing approach** - they're fast, reliable, and don't depend on external services
2. **Integration tests should be used for validation** - they help catch API changes and real-world issues
3. **External dependencies should be optional** - tests should skip gracefully if external services are unavailable
4. **CI/CD should focus on unit tests** - integration tests can be run separately or on-demand
