# Sentry: client UserOp failures as Warn (not Error)

**Date:** 2026-08-07  
**Branch:** `fix/sentry-client-userop-warn`  
**Context:** Studio testing against prod fired several `EIGENLAYER-AVS-*` issues
(Gas Manager declined / AA23 / no session grant / webhook deny).

## Re-evaluation vs staging (2026-08-07)

| Sentry class (2026-08-06) | Still holds? | Staging status | Prod (main 4.9.3) |
|---------------------------|--------------|----------------|-------------------|
| AA23 via Gas Manager (WETH sell / allowlist) | **Yes** as *product* root cause until grant is USDC+WETH | Preflight #711 on staging; typed `SESSION_POLICY_*` | Preflight **not** on main yet |
| Webhook deny (`Request was denied by webhook`) | **Mostly fixed** (chainId string #710) | On main 4.9.3 | No new events after 09:13Z on 4.9.2 |
| No session authorization | **Yes** when client has no grant | Fail-fast intentional | Same |
| Worker unsponsored | **Code fixed** #723 | On staging | **Not** on main |
| Multi-grant stacking | **Code fixed** #716 supersede | On staging | **Not** on main |
| Live fixture AA23 (#719) | **Partially** fixed #714/#720/#727 | Integration-tagged | N/A |

Sentry **last_seen** for the Aug 6 cluster is still those timestamps (no newer
events as of re-check). Issues remain **unresolved** in Sentry until closed.

## GitHub issues

| Issue | Relation | Action |
|-------|----------|--------|
| [#722](https://github.com/AvaProtocol/EigenLayer-AVS/issues/722) worker Gas Manager | Same sponsorship class | Closed (fixed by #723 on staging) |
| [#719](https://github.com/AvaProtocol/EigenLayer-AVS/issues/719) live fixture AA23 | Same AA23/fixture family | Open — fixture isolation remains |
| [#715](https://github.com/AvaProtocol/EigenLayer-AVS/issues/715) / [#717](https://github.com/AvaProtocol/EigenLayer-AVS/issues/717) grant replace / on-chain uninstall | Multi-grant / exposure | #716 landed replace; #717 still open |

## Code change in this branch

`LogBundlerError` previously only demoted **mined** `success=false` to Warn.
Studio-facing failures still used **Error** → Sentry:

- `no session authorization`
- `SESSION_POLICY_TARGET_NOT_ALLOWED` / multi-grant
- `cannot pay gas` (self-funded empty)
- `gas manager declined to sponsor` (webhook deny, AA23, sim revert)
- bare `AA23`

`IsClientUserOpFailure` classifies those as **Warn**. True infra (bundler dial,
unexpected RPC) stays **Error**.

## Follow-ups (not this PR)

1. **staging → main** release so preflight + worker policy + grant supersede hit prod.
2. Resolve Sentry issues after soak (2E AA23 may still occur until users re-grant WETH).
3. #717 on-chain uninstall of superseded entities.
