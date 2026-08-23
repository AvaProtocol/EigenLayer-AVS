package aggregator

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

func TestAliasForUsesFreshCacheWithoutSources(t *testing.T) {
	operator := common.HexToAddress("0xc6b87cc9e85b07365b6abefff061f237f7cf7dc3")
	alias := common.HexToAddress("0x4f061d46aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	r := withCachedAlias(operator.Hex(), alias.Hex())

	got, err := r.aliasFor(context.Background(), operator)
	if err != nil {
		t.Fatalf("fresh cache should not need a contract: %v", err)
	}
	if got != alias {
		t.Fatalf("got %s, want %s", got.Hex(), alias.Hex())
	}
}

func TestAliasForFallsBackToStaleCacheWhenSourcesFail(t *testing.T) {
	operator := common.HexToAddress("0xa026265a00000000000000000000000000000000")
	alias := common.HexToAddress("0x62086dea00000000000000000000000000000000")
	r := withCachedAlias(operator.Hex(), alias.Hex())
	r.cache[operator] = aliasEntry{alias: alias, fetchedAt: time.Now().Add(-time.Hour)}
	// A source with no contract is skipped; with a stale cache we must
	// still serve the last mapping rather than refuse the operator.
	r.sources = []aliasSource{{name: "ethereum"}}

	got, err := r.aliasFor(context.Background(), operator)
	if err != nil {
		t.Fatalf("stale cache must survive a dead source: %v", err)
	}
	if got != alias {
		t.Fatalf("got %s, want %s", got.Hex(), alias.Hex())
	}
}

func TestPingReportsDownSource(t *testing.T) {
	r := newOperatorAliasResolver([]aliasSource{{
		chainID: 1,
		name:    "ethereum",
		address: common.HexToAddress("0x9c02dfc92eea988902a98919bf4f035e4aaefced"),
	}}, testutil.GetLogger())

	got := r.ping(context.Background())
	if len(got) != 1 {
		t.Fatalf("got %d sources, want 1", len(got))
	}
	if got[0].Status != deepHealthDown {
		t.Fatalf("status %q, want %s", got[0].Status, deepHealthDown)
	}
}
