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



# AI Workflow Email Summary - Product Requirements (PRD)

## 1. Title and Overview
The goal is to produce clear, human-readable summaries of on-chain workflow executions. These summaries are delivered via email notifications (e.g., using SendGrid) when `options.summarize` is enabled, helping providers and users understand exactly what happened during automated on-chain workflows.

## 2. Execution Issues and Diagnosis
Earlier summary attempts were too minimal and lacked useful detail. This was due to:
- Digest context including only limited information (e.g., just workflow name/status, missing steps and actions)
- Strict model timeouts, leading to truncated or incomplete responses
- Insufficient normalization of on-chain actions, so summaries omitted critical contract interactions

## 3. Improved Design Requirements
The digest provided to the LLM must include:
- **Workflow**: Name, final status, and number of executed steps
- **Runner Smart Wallet**: The address that executed on-chain operations
- **Owner EOA**: The externally owned account associated with the workflow (derived from `TEST_PRIVATE_KEY`)
- **On-chain Actions**: All normalized contract interactions, including:
  - Contract address (and known name/purpose)
  - Method invoked (e.g., `approve`, `exactInputSingle`)
  - Key parameters and outputs (token addresses/symbols, formatted amounts)
  - For swaps, explicitly state which tokens were spent and received, and in what amounts

## 4. New Prompt Specification
**System Prompt for the Model:**
```
You are a helpful assistant that summarizes on-chain workflow executions for human readers. Given the following JSON digest, produce a concise subject line and a detailed body explaining what happened, focusing on the workflow, runner, owner EOA, and all on-chain actions (approvals, swaps, quotes, etc). Output MUST be a JSON object:
{
  "subject": "...",
  "body": "..."
}

Examples:
Digest:
{
  "workflow": {"name": "Uniswap V3 Stoploss", "status": "success", "steps": 7},
  "runner": "0x1234...abcd",
  "owner_eoa": "0xabcd...1234",
  "actions": [
    {
      "type": "approve",
      "contract": {"address": "0x1c7D4...7238", "name": "USDC"},
      "spender": {"address": "0x3bFA4...48E", "name": "SwapRouter02"},
      "amount": "10000"
    },
    {
      "type": "quote",
      "contract": {"address": "0xEd1f6...2FB3", "name": "QuoterV2"},
      "method": "quoteExactInputSingle",
      "params": {"tokenIn": "USDC", "tokenOut": "WETH"},
      "amountOut": "780448437916"
    },
    {
      "type": "swap",
      "contract": {"address": "0x3bFA4...48E", "name": "SwapRouter02"},
      "method": "exactInputSingle",
      "spent": {"token": "USDC", "amount": "10000"},
      "received": {"token": "WETH", "amount": "780448437916"}
    }
  ]
}
Return only the JSON object with subject/body. Do not add explanations or markdown.
```

## 5. Code Snippet Example (Normalized Actions JSON)
```go
actions := []Action{
  {
    Type: "approve",
    Contract: ContractInfo{Address: "0x1c7D4...7238", Name: "USDC"},
    Spender: &ContractInfo{Address: "0x3bFA4...48E", Name: "SwapRouter02"},
    Amount: "10000",
  },
  {
    Type: "quote",
    Contract: ContractInfo{Address: "0xEd1f6...2FB3", Name: "QuoterV2"},
    Method: "quoteExactInputSingle",
    Params: map[string]string{"tokenIn": "USDC", "tokenOut": "WETH"},
    AmountOut: "780448437916",
  },
  {
    Type: "swap",
    Contract: ContractInfo{Address: "0x3bFA4...48E", Name: "SwapRouter02"},
    Method: "exactInputSingle",
    Spent: &TokenAmount{Token: "USDC", Amount: "10000"},
    Received: &TokenAmount{Token: "WETH", Amount: "780448437916"},
  },
}
```

## 6. Ideal Output Example
**Sample Subject/Body:**
```json
{
  "subject": "Uniswap V3 Stoploss workflow succeeded (7 steps)",
  "body": "The workflow 'Uniswap V3 Stoploss' completed successfully in 7 steps. The runner smart wallet 0x1234...abcd executed the transactions on behalf of owner EOA 0xabcd...1234. First, USDC (0x1c7D4...7238) was approved for SwapRouter02 (0x3bFA4...48E) with an amount of 10,000. Then, a quote was obtained from QuoterV2 (0xEd1f6...2FB3) for swapping USDC to WETH, resulting in an amount out of 780,448,437,916 (raw units). Finally, a swap was performed via SwapRouter02, spending 10,000 USDC and receiving 780,448,437,916 WETH (raw units)."
}
```

## 7. Implementation Notes
- **Timeouts**: Increase model timeout to ensure the LLM can process the full digest and return a complete summary; avoid overly strict deadlines.
- **Fallbacks**: On any AI/model error or missing keys in the output, always fall back to a deterministic summary.
- **Schema Validation**: Validate that the LLM output is a JSON object with both `subject` and `body` fields. Reject and fallback if not.
- **Digest Construction**: Always include all required fields (workflow, runner, owner, normalized actions) in the digest for the prompt.
- **Injection Point**: In `vm_runner_rest.go`, call `ComposeSummarySmart` and inject the resulting `subject`/`body` into the outgoing provider payload if `options.summarize` is true.

## 8. Testing/Validation
- **Subject/Body Quality**: Ensure that the generated subject and body are present, non-empty, and meet a minimum length threshold (e.g., subject ≥ 10 chars, body ≥ 40 chars).
- **Tokenized Action Info**: Parse the body and verify that all normalized actions (approvals, swaps, quotes) are mentioned with correct token names and amounts.
- **Fallback Handling**: Simulate model failures and verify that the deterministic fallback summary is used and contains the essential workflow/action information.
- **EOA/Runner Derivation**: In tests, load `.env` and derive the owner EOA from `TEST_PRIVATE_KEY`; use this to compute the runner smart wallet (salt:0) via the AA factory.
- **API Key Loading**: Ensure OpenAI key is loaded from aggregator config macro secrets first, else from `OPENAI_API_KEY` env variable.