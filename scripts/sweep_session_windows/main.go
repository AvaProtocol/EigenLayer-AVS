// sweep_session_windows — find stored session grants whose TimeRangeModule
// window disagrees with SessionPolicy.ValidUntil (#763 sweep).
//
// A+B close allocation going forward; they cannot heal a grant that is
// already stored wrong. This walk is how operators find those records so
// the owner can re-grant onto a free entity. It does not revoke or re-grant
// — the owner signature cannot be produced here.
//
// READ-ONLY in intent, but BadgerDB takes an exclusive directory lock, so
// run against a COPY/backup of the gateway DB, not the live directory.
//
// Usage:
//
//	go run ./scripts/sweep_session_windows \
//	  --gateway-path /path/to/gateway-db-copy \
//	  --chain 11155111 \
//	  --rpc "$ETH_RPC_URL"
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "sweep failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	gatewayPath := flag.String("gateway-path", "", "Path to a COPY of the gateway BadgerDB directory (not the live DB)")
	chainID := flag.Int64("chain", 0, "Chain id to scan (e.g. 11155111)")
	rpcURL := flag.String("rpc", "", "HTTP RPC URL for TimeRangeModule.timeRanges reads")
	flag.Parse()
	if *gatewayPath == "" || *chainID <= 0 || *rpcURL == "" {
		return fmt.Errorf("--gateway-path, --chain, and --rpc are required")
	}

	db, err := storage.NewWithPath(*gatewayPath)
	if err != nil {
		return fmt.Errorf("open BadgerDB at %s: %w", *gatewayPath, err)
	}
	defer db.Close()

	client, err := ethclient.Dial(*rpcURL)
	if err != nil {
		return fmt.Errorf("dialing RPC: %w", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	verify := func(ctx context.Context, _ int64, account common.Address, entity uint32) (uint64, uint64, error) {
		return aa.EntityTimeRangeOnChain(ctx, client, account, entity)
	}

	drifted, err := taskengine.FindDriftedSessionGrants(ctx, db, *chainID, verify)
	if err != nil {
		return err
	}

	if len(drifted) == 0 {
		fmt.Printf("no drifted session grants on chain %d\n", *chainID)
		return nil
	}

	fmt.Printf("drifted session grants on chain %d: %d\n", *chainID, len(drifted))
	missing := 0
	for _, d := range drifted {
		p := d.Policy
		runner := ""
		if p.Runner != nil {
			runner = p.Runner.Hex()
		}
		if d.ReadErr != nil {
			fmt.Printf("  policy=%s entity=%d runner=%s storedUntil=%d read_error=%v\n",
				p.ID, p.EntityID, runner, d.StoredUntilSec, d.ReadErr)
			continue
		}
		if d.WindowMissing {
			// Printing chainUntil here would render as 1970-01-01 and read as a
			// corrupt date rather than "the hook was never installed".
			fmt.Printf("  policy=%s entity=%d runner=%s storedUntil=%s chainUntil=NO HOOK INSTALLED\n",
				p.ID, p.EntityID, runner,
				time.Unix(int64(d.StoredUntilSec), 0).UTC().Format(time.RFC3339))
			missing++
			continue
		}
		fmt.Printf("  policy=%s entity=%d runner=%s storedUntil=%s chainUntil=%s\n",
			p.ID, p.EntityID, runner,
			time.Unix(int64(d.StoredUntilSec), 0).UTC().Format(time.RFC3339),
			time.Unix(int64(d.ChainUntilSec), 0).UTC().Format(time.RFC3339))
	}
	if missing > 0 {
		// This is the number that decides whether it is safe to deploy: these
		// are the grants the send path will begin refusing.
		fmt.Printf("\n%d of %d have NO TimeRange hook installed.\n", missing, len(drifted))
		fmt.Printf("Those are refused as %s once this build is live — re-grant them first.\n",
			taskengine.SessionPolicyChainWindowMissingCode)
	}
	log.Printf("re-grant each drifted runner onto a free entity; this script does not write")
	return nil
}
