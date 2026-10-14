# Size self-funded gas into native session grants

- **Date**: 2026-10-14
- **Status**: Implemented
- **Branch**: fix/native-cap-self-funded-gas
- **Related**: AvaProtocol/EigenLayer-AVS#836, docs/changes/20260917-native-eth-and-eoa-permissions.md

## Problem

Weekly ETH Pay sizes `nativeSpendCap` as amount × runs × recipients. On a self-funded chain, NativeTokenLimitModule charges gas and value from that one cap, including the first UserOp that installs the module. A send of exactly the cap fails once gas is added. The same cap also meters every other self-funded UserOp on the wallet, so a weekly USDC pay on the same runner starts failing `SESSION_POLICY_NATIVE_CAP_EXCEEDED` after the owner adds ETH pay.

A few other gaps block the Skill at the same time. Simulate can report covered when a recipient is missing, because a low balance takes the else branch and the verdict only compared cap amounts. A sixth recipient, a contract, and a 7702-delegated EOA come back as `POLICIES_REJECTED` detail strings. An applied grant re-checks `eth_getCode` on every run, so a recipient that later delegates fails a week the install already allowed. A JSON number above 2^53 wei is unsized with no hint to send a decimal string. A cron grant with no `expiredAt` expires at the last fire, so that run is `SESSION_POLICY_EXPIRED`. The local ethTransfer simulation used a gas figure far under a Modular Account v2 UserOp. The fee estimator's display number is a different job and stays the smaller figure.

## Decision

Price gas only when the chain is unsponsored. `SponsorshipPolicyID() == ""` passes `WithSelfFundedGrantFee`; a nil fee leaves the cap as the ETH the workflow sends. Production Ethereum, Base, Sepolia, and Base Sepolia share one gateway `alchemy_paymaster_policy_id` and do not set `disable_gas_sponsorship`, so sponsored Weekly ETH Pay caps stay amount × runs × recipients. The unsponsored path is what local gateway and any empty-policy deploy hit.

The fee is a documented per-chain assumption, not a live base fee at prepare, so prepare and submit match and a 12-week grant is not sized off a quiet hour:

| Chain | Wei per gas |
| --- | --- |
| Ethereum (1) | 50 gwei |
| Sepolia (11155111) | 2 gwei |
| Base (8453) | 1 gwei |
| Base Sepolia (84532) | 0.1 gwei |
| Any other chain | 2 gwei |

Per task, units are `sendOps × 500_000 + valueOps × 1_500_000`, plus one install premium: 1.5M when the task has a payable write (`3M − 1.5M`), otherwise 1.5M for an ethTransfer (`2M − 500k`). Several automations on one wallet keep only the largest premium. An ethTransfer loop is one UserOp per recipient per run. A deployed contract write is one UserOp per method call. Run-node still batches those calls into one UserOp; the grant is sized for the deployed path, so a batched run-node is slightly over-granted. Unknown run counts add a summary sentence and no guessed wei. A zero-value write does not create a native cap by itself. Sibling contract-write gas is included when a native cap already exists or this merge installs one. ERC-20-only grants stay without a native module.

Prepare, submit, simulate, deploy check, and drift explanation all call `priceWalletNativeGas` with that same fee. Classify prices the wallet's needs once, so two installs are not required of a grant that kept one. The summary says the cap includes self-funded gas, and it says "one install" only when this merge kept that premium. That headroom can also be sent as value to the allowed recipients. A live fee above the assumption can still return `SESSION_POLICY_NATIVE_CAP_EXCEEDED`. Preflight still compares the granted total. This change does not read the module's remaining limit on every send.

Studio prepares one draft at a time. Simulate prices after the selected policy is known and after observed spend has filled the cap. The draft's cap is steady gas plus value. It does not include the wallet's one install, because prepare adds that once, and an already-applied grant does not pay it again. `required.nativeSpendCap` therefore already contains the draft's steady gas, which is what Studio forwards as the addition today. An ERC-20 draft on a runner that already has a native cap gets a native cap of its own steady gas and no recipients. Deploy check prices after the locked policy lookup and forces gas on the same rule. A value-only addition that simulate did not price, with no enabled tasks to count, still receives only the install: the addition has no op counts, and this change does not invent them.

`nativeGasWei` on the simulate need, the addition, and the prepare changes is the gas portion of the signed cap. It is not added on top of `nativeSpendCap`. Prepare's `changes.nativeGasWei` is the steady gas of enabled tasks, one install when the grant is not yet applied, plus `nativeGasWei` echoed on the addition. Until Studio echoes that field, a first grant's changes line is only the install prepare added; the draft's steady gas is inside the addition total and on the simulate verdict.

Sponsorship is read when the grant is prepared. If it is later turned off, an existing value-only cap starts paying gas and runs short (`SESSION_POLICY_NATIVE_CAP_EXCEEDED`) until the owner prepares again.

Simulate compares `need.NativeRecipients` with the grant even when the run took the else branch. Unresolved targets and `cap_needs_input` still outrank that check. More than five recipients is `SESSION_POLICY_TOO_MANY_NATIVE_RECIPIENTS`. A contract or 7702 recipient at install is `SESSION_POLICY_RECIPIENT_NOT_EOA`. Both are HTTP 400. The recipient list still only grows; finished automations keep their addresses until revoke. Once `Grant.Applied()` is set, preflight skips the code check.

`json.Number` sizes exactly. An imprecise `float64` stays unsized and the detail says to send a decimal string. Protobuf has already turned a JSON number into `float64` before that, so the lost precision is not recovered. A cron with `maxExecution` and no `expiredAt` sets `validUntil` to 24 hours after the last fire. `expiredAt` is unchanged, and `Expired()` is unchanged.

The fee estimator display stays 150,000 for a contract write and 50,000 for an ETH transfer, including one loop iteration. Those numbers are what the deploy page shows, including on chains that sponsor gas. Cap sizing uses the higher ceilings. The ethTransfer simulation path uses the 500k ceiling, and its hash is still a local stand-in. There is no Tenderly call on that path.

## Alternatives considered

- A live `eth_feeHistory` at prepare. Prepare and submit could disagree, and a quiet hour would under-size the rest of the schedule.
- `DefaultGasPriceByChain` or the 2 gwei signed-op floor as the grant fee. The mainnet simulation price is 5 gwei, which is under what a self-funded cap has to survive. The floor is not NativeTokenLimitModule's signed max fee.
- Reading the module's remaining limit before every send. The product decision for v1 is to talk about the granted total. Gas headroom is the fix for the send-of-exactly-the-cap failure.
- Failing closed when a sibling's run count is unknown. The owner can still approve; the summary says those runs spend the cap without being included.
- Rewriting ethTransfer simulation onto Tenderly in this change. The gas figure was the under-report. The fabricated receipt stays.
- Showing the preflight ceilings on the fee estimator. That number is the deploy-page estimate for every workflow, including sponsored ones. The cap uses the ceilings. The display stays 150k / 50k.
- A typed error for the two new recipient codes, matched with `errors.As`. The codes are already stable prefixes of the `Validate` error, which is how `mapPolicyError` classifies the other policy strings. A typed error would not change the HTTP 400.

## Verification

`CGO_ENABLED=0 go test ./core/taskengine/` (unit tests, no integration tag) and the policies error-mapping test. Cases cover the fee table, one install across two ETH tasks, sibling USDC gas, an ERC-20-only grant, unknown runs, a carried remainder, a missing simulate recipient, `json.Number`, the 24-hour slack, the typed recipient codes, and an applied grant skipping the code check. Review cases cover ETH then USDC and USDC then ETH both landing on the same cap as one merged prepare, an applied grant not paying a second install, one UserOp per contract-write method, and simulate pricing an ERC-20 draft and an observed native amount only after the cap exists.

Not in this change: a live Sepolia schedule of loop + cron + native grant plus a second ERC-20 automation on the same runner. Run that before Studio enables Weekly ETH Pay on mainnet. No storage or protobuf field rename. `nativeGasWei` is an additive OpenAPI field.
