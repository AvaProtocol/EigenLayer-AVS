## Chainlink price feeds on Sepolia: proxy vs aggregator, and how we consume updates

### TL;DR
- Read prices from the proxy via `latestRoundData()` and `decimals()`.
- Listen for updates on the OCR aggregator by subscribing to the `AnswerUpdated(int256,uint256,uint256)` event topic, then fetch from the proxy.
- The proxy is upgrade-stable; the aggregator can rotate. If you subscribe to events, resolve the current aggregator from the proxy and refresh on rotation.

### Architecture
- **Proxy (AggregatorV3Interface)**
  - Stable address clients should read from.
  - Implements v3 interface and forwards reads to the current aggregator.
  - Does not generally emit price update events.

- **Aggregator (OCR implementation)**
  - Receives offchain reports via `transmit` transactions.
  - Emits logs such as `AnswerUpdated(int256 current, uint256 roundId, uint256 updatedAt)` as part of those transmissions.
  - Can be rotated; the proxy’s `aggregator()` points to the current implementation.

On Sepolia (example feed used in our tests):
- Proxy: `0x694AA1769357215DE4FAC081bf1f309aDC325306`  
  Reference: https://sepolia.etherscan.io/address/0x694AA1769357215DE4FAC081bf1f309aDC325306#events
- Aggregator (current at time of writing): `0x719E22E3D4b690E5d96cCb40619180B5427F14AE`  
  Reference: https://sepolia.etherscan.io/address/0x719E22E3D4b690E5d96cCb40619180B5427F14AE#events

### Event-driven consumption (recommended pattern)
1. Resolve the current aggregator at runtime:
   - Call `aggregator()` on the proxy to get the OCR aggregator address.
2. Subscribe to the aggregator’s `AnswerUpdated` event:
   - topic0 = keccak256("AnswerUpdated(int256,uint256,uint256)")
   - Hash: `0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f`
3. On every `AnswerUpdated` log:
   - Call the proxy’s `latestRoundData()` and `decimals()`.
   - Apply workflow conditions to the returned answer (scale by `10^decimals`).
4. Handle aggregator rotation:
   - Either listen for the proxy’s aggregator-change event (e.g., `AggregatorUpdated`) or periodically re-call `aggregator()` and update the subscription if it changes.

Notes:
- Do not parse the price directly from event logs. Always read via the proxy to preserve upgrade safety and correct scaling.
- Sepolia feeds are test feeds; update cadence can be sporadic.

### Polling alternative
If event cadence is insufficient or you prefer simplicity, use a block/cron trigger to periodically call the proxy’s `latestRoundData()` (and `decimals()`) and evaluate conditions. This avoids managing aggregator rotations and subscriptions.

### Implementation hints (operator)
- Build an Ethereum filter with:
  - `Addresses = [currentAggregator]`
  - `Topics[0] = 0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f`
- On match:
  - `eth_call` to proxy `latestRoundData()`; compare the `answer` to your thresholds after scaling by `decimals()`.
- Re-resolve aggregator when the proxy indicates an update or on a periodic timer.

### References
- Proxy (read/events): https://sepolia.etherscan.io/address/0x694AA1769357215DE4FAC081bf1f309aDC325306#events
- Aggregator (events): https://sepolia.etherscan.io/address/0x719E22E3D4b690E5d96cCb40619180B5427F14AE#events

