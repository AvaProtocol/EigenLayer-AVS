# Carry standard permission routes into wallet budgets

Studio's legacy-permission migration retains Aave V3's
`setUserUseReserveAsCollateral(address,bool)` call. The budget route check
previously rejected it even on the catalog Pool. The first-budget compiler also
includes Lido's `submit(address)`, which the route check rejected despite the
existing budget classification supporting it.

The route check now accepts those exact selectors on their chain's catalog
contracts. Collateral configuration remains disclosed as an unmetered action.
Lido staking is accepted only with a native spending limit. No changes are made
to token-cap validation, unknown contract refusal or owner signing.

Review follow-up: native-recipient eligibility is separate from selector-scoped
route approval. The previously admitted Pool, router and wrapped-native targets
remain eligible. Lido is refused as an unrestricted native recipient even when
a native limit exists; native recipient encoding would otherwise allow every
selector without an ERC-20 limit or unmetered-action disclosure.

A zero native limit remains valid and keeps both enforcement hooks installed.
The existing integration test
`TestWalletBudgetRouterWithoutWETHWrapPermissions_Sepolia/S5ETHToToken` verifies
that the same native module charges contract-call value plus gas without a
native-recipient entry. The new regression checks zero-limit hook retention;
no Lido mainnet transaction was sent.

The companion Studio fix must omit a WETH cap when retaining existing direct
wrapping/unwrapping rights. Adding a cap while retaining those selectors remains
invalid, and the regression asserts that refusal as well as the valid migration.

The full legacy Ethereum scope is exercised through merge, permission validation
and route validation. Tests also reject uncatalogued targets, an unrelated Lido
selector, wrong-chain targets, uppercase selectors, and staking without a native
limit. The budget/wallet-budget unit suite passes with an ephemeral offline
bootstrap RPC and dummy test configuration.
No production configuration, wallet state or transaction was changed.
