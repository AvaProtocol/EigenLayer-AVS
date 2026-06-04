#!/usr/bin/env bash
#
# run-merge-rehearsal.sh — exercise the Hetzner→gateway merge tool against
# locally-snapshotted donor BadgerDBs.
#
# Reads ./donors/<chain>/db/, runs the merge tool sequentially per chain
# into a scratch gateway DB, prints a per-chain summary table, then
# reports aggregate counts the operator can compare to the live SDK
# output afterwards.
#
# Usage:
#   scripts/dev/run-merge-rehearsal.sh                    # all 4 chains
#   scripts/dev/run-merge-rehearsal.sh sepolia            # one chain
#   scripts/dev/run-merge-rehearsal.sh sepolia base-sepolia
#
# Run order matters when multiple chains are requested: sepolia, base-
# sepolia, base, ethereum (smallest first → catch issues before paying
# the wall-clock cost of mainnet). Override the order by passing chains
# in your preferred sequence.
#
# Prereqs:
#   - Donor data already at ./donors/<chain>/db/ (run snapshot-hetzner-donors.sh first)
#   - Local Go toolchain (`go run ./scripts/migration/merge_hetzner_into_gateway/...`)
#
# Exit codes:
#   0  every requested chain merged + summary printed
#   1  prereq missing or donor data not snapshotted
#   2  the merge tool errored on one or more chains
#
# This is the DRY-RUN by default. To actually write to the scratch
# gateway DB, pass --apply as the first arg:
#
#   scripts/dev/run-merge-rehearsal.sh --apply
#   scripts/dev/run-merge-rehearsal.sh --apply sepolia

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DONORS_DIR="${ROOT_DIR}/donors"
GATEWAY_DB="${ROOT_DIR}/tmp/rehearsal-gateway-db"

declare -A CHAIN_IDS
CHAIN_IDS[sepolia]="11155111"
CHAIN_IDS[base-sepolia]="84532"
CHAIN_IDS[ethereum]="1"
CHAIN_IDS[base]="8453"

DRY_RUN="true"
if [[ "${1:-}" == "--apply" ]]; then
    DRY_RUN="false"
    shift
fi

# Default ordering: smallest first so iteration cycles are short
DEFAULT_ORDER="sepolia base-sepolia base ethereum"

if [[ $# -eq 0 ]]; then
    CHAINS="${DEFAULT_ORDER}"
else
    CHAINS="$*"
fi

for chain in ${CHAINS}; do
    if [[ -z "${CHAIN_IDS[${chain}]:-}" ]]; then
        echo "ERROR: unknown chain '${chain}' — pick from: ${DEFAULT_ORDER}" >&2
        exit 1
    fi
    if [[ ! -d "${DONORS_DIR}/${chain}/db" ]]; then
        echo "ERROR: no donor snapshot for ${chain} at ${DONORS_DIR}/${chain}/db" >&2
        echo "       run: scripts/dev/snapshot-hetzner-donors.sh ${chain}" >&2
        exit 1
    fi
done

# Fresh scratch gateway DB every rehearsal — we want to know exactly
# what each merge contributed, not what stacked on top of a prior run.
echo "Resetting scratch gateway DB: ${GATEWAY_DB}"
rm -rf "${GATEWAY_DB}"
mkdir -p "${GATEWAY_DB}"

echo
echo "Mode:    $([[ "${DRY_RUN}" == "true" ]] && echo "DRY RUN (no writes)" || echo "APPLY (writes to scratch gateway)")"
echo "Chains:  ${CHAINS}"
echo

FAILED=()

for chain in ${CHAINS}; do
    chain_id="${CHAIN_IDS[${chain}]}"
    donor_path="${DONORS_DIR}/${chain}/db"

    echo "================================================================"
    echo "MERGE: ${chain} (chain_id=${chain_id})"
    echo "================================================================"

    if ! ( cd "${ROOT_DIR}" && go run ./scripts/migration/merge_hetzner_into_gateway \
        --donor-path     "${donor_path}" \
        --donor-chain-id "${chain_id}" \
        --gateway-path   "${GATEWAY_DB}" \
        --dry-run="${DRY_RUN}" ); then
        echo "FAIL: merge tool errored on ${chain}" >&2
        FAILED+=("${chain}")
    fi
    echo
done

if [[ "${DRY_RUN}" == "false" ]]; then
    echo "================================================================"
    echo "POST-MERGE KEY COUNTS (gateway DB: ${GATEWAY_DB})"
    echo "================================================================"
    # Print per-prefix counts using badger info (if available) or a
    # one-shot Go scan via the merge tool's IterateKeysOnly.
    ( cd "${ROOT_DIR}" && go run scripts/dev/count-gateway-keys.go --gateway-path "${GATEWAY_DB}" ) || \
        echo "(count tool not available — skip)"
fi

if [[ ${#FAILED[@]} -gt 0 ]]; then
    echo "FAILED CHAINS: ${FAILED[*]}" >&2
    exit 2
fi

echo
echo "Rehearsal complete."
echo
if [[ "${DRY_RUN}" == "true" ]]; then
    echo "This was a DRY RUN. Re-run with --apply to actually write to ${GATEWAY_DB}."
    echo "Then start the dev gateway against that DB with:"
    echo "  make dev-stack-rehearsal"
fi
