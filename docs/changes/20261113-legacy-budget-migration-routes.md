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

The companion Studio fix must omit a WETH cap when retaining existing direct
wrapping/unwrapping rights. Adding a cap while retaining those selectors remains
invalid, and the regression asserts that refusal as well as the valid migration.

The full legacy Ethereum scope is exercised through merge, permission validation
and route validation. Tests also reject uncatalogued targets, an unrelated Lido
selector, and staking without a native limit. All 142 budget/wallet-budget unit
tests pass with an ephemeral offline bootstrap RPC and dummy test configuration.
No production configuration, wallet state or transaction was changed.
