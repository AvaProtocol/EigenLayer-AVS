### AI Workflow Email Summary - Product Requirements (PRD)

#### Overview
Generate a human-readable email summary for workflow executions that explains what happened on-chain, not just whether it succeeded. This is used in provider notifications (e.g., SendGrid) when `options.summarize` is enabled.

#### Inputs and References
- Workflow config: `EigenLayer-AVS/uniswapv3-stoploss.json`
- Simulated execution: `EigenLayer-AVS/simulate-workflow.log`
- VM context: `aa_sender`, `workflowContext.runner`, `workflowContext.owner`, and `Execution_Step` logs

#### Summary Content Requirements
- **Workflow**
  - Name and final status
  - Number of executed steps
- **Runner Smart Wallet**
  - Address of the smart wallet that executed on-chain operations
- **Owner EOA**
  - EOA associated with the smart wallet (derived from `TEST_PRIVATE_KEY` in `.env`)
- **On-chain Actions (contractRead/contractWrite)**
  - Contract address (and purpose/name if known)
  - Method invoked (e.g., `approve`, `quoteExactInputSingle`, `exactInputSingle`)
  - Key parameters and results (token addresses/symbols, amounts in/out)
  - For swaps, explicitly state “spent {token1} amount for {token0} amount”

#### Behavior
- Compose summary using AI when enabled; otherwise use deterministic fallback.
- Inject the composed summary into REST provider payloads when `options.summarize` is true.

#### AI Summarization
- **Provider/Model**: OpenAI `gpt-4o-mini`
- **Configuration Source**:
  - Preferred: `macros.secrets.openai_api_key` in aggregator config (pattern matches Moralis key)
  - Fallback: `OPENAI_API_KEY` env var for local/dev runs
- **Prompt/Digest**
  - Build a compact JSON digest from VM and execution logs including: workflow name, status, steps, runner smart wallet, owner EOA, and contract call specifics (address, method, params, outputs)
  - Prompt explicitly asks for workflow/steps, runner, owner EOA, and on-chain actions (approvals, quotes, swaps)
- **Timeout/Guardrails**
  - Respect configured `timeout_ms`, token limits, and budget guards
  - On any AI error or missing key, fall back to deterministic summary

#### Deterministic Fallback
- Always available; never fails execution
- Should provide a useful summary including: workflow name/status/steps and best-effort extraction of key contract interactions (address, method, key params/outputs)

#### Test/Dev Requirements
- **EOA Derivation**: Load `.env` and derive the owner EOA from `TEST_PRIVATE_KEY` (no hard-coded fallbacks). Use the derived EOA to compute the smart wallet (salt:0) via AA factory.
- **Key Loading (tests)**: Retrieve the OpenAI key using the same mechanism as other API keys (e.g., Moralis) — first from aggregator config macro secrets, else from `OPENAI_API_KEY`.
- **Golden Example (from provided workflow/logs)**
  - Workflow “Using sdk test wallet” completed successfully with 7 steps
  - Runner smart wallet executed the calls; owner EOA derived from `TEST_PRIVATE_KEY`
  - Approve USDC `0x1c7D4...7238` for spender SwapRouter02 `0x3bFA4...48E` with value `10000`
  - Quote via QuoterV2 `0xEd1f6...2FB3`: `quoteExactInputSingle` → `amountOut = 780448437916`
  - Swap via SwapRouter02 `0x3bFA4...48E`: `exactInputSingle` — spent USDC for WETH, received `780448437916` (raw units)

#### Injection Points
- In `vm_runner_rest.go`, when sending to providers and `options.summarize` is true, call `ComposeSummarySmart` and inject the `Subject`/`Body` into the outgoing request payload

#### Non-Goals
- No fallback to Tenderly simulation for deployed workflows when bundler fails
- Do not generate calldata on the client; do not change existing SDK transport semantics


