#!/usr/bin/env bash
#
# snapshot-hetzner-donors.sh — pull a fresh BadgerDB tarball from each
# Hetzner aggregator and extract it to ./donors/<chain>/db/.
#
# Used by the migration rehearsal flow (see scripts/dev/run-merge-rehearsal.sh
# and `make migration-rehearse`). Captures the live aggregator state with a
# brief docker-stop so the BadgerDB on-disk snapshot is internally
# consistent.
#
# Usage:
#   scripts/dev/snapshot-hetzner-donors.sh                    # all 4 chains
#   scripts/dev/snapshot-hetzner-donors.sh sepolia            # one chain
#   scripts/dev/snapshot-hetzner-donors.sh sepolia base-sepolia
#
# Each chain's data lands at ./donors/<chain>/db/ (relative to the
# project root). Existing data is overwritten — the script always pulls
# fresh.
#
# Downtime: each aggregator is stopped for the duration of its tarball
# (~30s for testnets, ~60-90s for mainnets). Mainnet downtime moves into
# the actual maintenance window after this rehearsal succeeds.
#
# Prereqs:
#   - SSH access to ap-prod1 (mainnet) and ap-staging1 (testnet)
#   - ~/.ssh/config aliases set up
#   - docker available on each host
#
# Exit codes:
#   0  every requested chain snapshotted + extracted OK
#   1  prereq missing (ssh, tar, etc.)
#   2  one or more chains failed — partial download left in ./donors/

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DONORS_DIR="${ROOT_DIR}/donors"

# Chain → (host, container) map. Container name is also the chain folder.
declare -A HOSTS
HOSTS[sepolia]="ap-staging1"
HOSTS[base-sepolia]="ap-staging1"
HOSTS[ethereum]="ap-prod1"
HOSTS[base]="ap-prod1"

ALL_CHAINS="sepolia base-sepolia ethereum base"

# Pick which chains to snapshot
if [[ $# -eq 0 ]]; then
    CHAINS="${ALL_CHAINS}"
else
    CHAINS="$*"
fi

# Validate every chain is known before doing any work
for chain in ${CHAINS}; do
    if [[ -z "${HOSTS[${chain}]:-}" ]]; then
        echo "ERROR: unknown chain '${chain}' — pick from: ${ALL_CHAINS}" >&2
        exit 1
    fi
done

mkdir -p "${DONORS_DIR}"
echo "Snapshot destination: ${DONORS_DIR}"
echo "Chains:               ${CHAINS}"
echo

FAILED=()

for chain in ${CHAINS}; do
    host="${HOSTS[${chain}]}"
    container="aggregator-${chain}"
    out_dir="${DONORS_DIR}/${chain}"
    tar_file="${DONORS_DIR}/${chain}.tar.gz"

    echo "=== ${chain} (${host}:${container}) ==="

    # Stop, tarball via docker exec on a temporary alpine container that
    # mounts the same volume, restart. The trap-on-EXIT ensures the
    # aggregator restarts even if tar fails mid-stream.
    #
    # We use --volumes-from on a throwaway alpine container so we don't
    # rely on the host-side mount path (which differs between ap-prod1
    # and ap-staging1).
    echo "  -> stop, tarball via --volumes-from, restart"
    if ! ssh -o BatchMode=yes "${host}" "
        set -e
        docker stop ${container} >/dev/null
        trap 'docker start ${container} >/dev/null' EXIT
        docker run --rm --volumes-from ${container} alpine \
            tar czf - -C /tmp/ap-avs db
    " > "${tar_file}" 2>/dev/null; then
        echo "  FAIL: ssh+tar pipeline failed for ${chain}" >&2
        FAILED+=("${chain}")
        rm -f "${tar_file}"
        continue
    fi

    size=$(du -h "${tar_file}" | cut -f1)
    echo "  -> tarball: ${tar_file} (${size})"

    # Extract into ./donors/<chain>/ — wipe any prior extraction first
    rm -rf "${out_dir}"
    mkdir -p "${out_dir}"
    if ! tar xzf "${tar_file}" -C "${out_dir}"; then
        echo "  FAIL: tar extract failed for ${chain}" >&2
        FAILED+=("${chain}")
        continue
    fi

    db_size=$(du -sh "${out_dir}/db" 2>/dev/null | cut -f1)
    echo "  -> extracted: ${out_dir}/db (${db_size})"

    # Drop the intermediate tarball — the extracted dir is what the
    # merge tool reads. Keeping both wastes disk.
    rm -f "${tar_file}"

    echo "  OK"
    echo
done

if [[ ${#FAILED[@]} -gt 0 ]]; then
    echo "FAILED: ${FAILED[*]}" >&2
    exit 2
fi

echo "All snapshots complete."
echo
echo "Next: scripts/dev/run-merge-rehearsal.sh"
