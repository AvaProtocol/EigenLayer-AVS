package aggregator

import (
	"context"
	"fmt"
	"strings"
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

func TestMergeAliasSourcesKeepsMainnetWhenSepoliaHasNoCode(t *testing.T) {
	logger := testutil.GetLogger()
	mainnet := aliasSource{
		chainID: 1,
		name:    "ethereum",
		address: common.HexToAddress("0x9c02dfc92eea988902a98919bf4f035e4aaefced"),
	}
	sepoliaErr := fmt.Errorf("no contract code at APConfig 0xB8aBBB082eCAAe8d1Cd68378cf3b060f6f0E07eb on avs (chain 11155111)")

	got, err := mergeAliasSources(logger,
		aliasBindAttempt{name: "avs", err: sepoliaErr},
		aliasBindAttempt{name: "ethereum", src: mainnet},
	)
	if err != nil {
		t.Fatalf("mainnet APConfig must be enough to start: %v", err)
	}
	if len(got) != 1 || got[0].name != "ethereum" {
		t.Fatalf("got %+v, want the mainnet source only", got)
	}
}

func TestMergeAliasSourcesFailsWhenNothingBound(t *testing.T) {
	logger := testutil.GetLogger()
	sepoliaErr := fmt.Errorf("no contract code at APConfig 0xB8aBBB082eCAAe8d1Cd68378cf3b060f6f0E07eb on avs (chain 11155111)")

	_, err := mergeAliasSources(logger, aliasBindAttempt{name: "avs", err: sepoliaErr})
	if err == nil {
		t.Fatal("expected startup to fail when no APConfig source bound")
	}
	if !strings.Contains(err.Error(), "no APConfig source bound") {
		t.Fatalf("error %q should say no source bound", err)
	}
	if !strings.Contains(err.Error(), sepoliaErr.Error()) {
		t.Fatalf("error %q should wrap the last bind failure", err)
	}
}

func TestMergeAliasSourcesKeepsEverySuccessfulBind(t *testing.T) {
	logger := testutil.GetLogger()
	avs := aliasSource{chainID: 11155111, name: "avs"}
	eth := aliasSource{chainID: 1, name: "ethereum"}

	got, err := mergeAliasSources(logger,
		aliasBindAttempt{name: "avs", src: avs},
		aliasBindAttempt{name: "ethereum", src: eth},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sources, want 2", len(got))
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
