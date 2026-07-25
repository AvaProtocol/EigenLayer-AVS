package macros

import (
	"math/big"
	"testing"
	"time"
)

func TestValidateChainlinkRound(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	maxAge := time.Hour
	tenMinAgo := big.NewInt(now.Add(-10 * time.Minute).Unix())
	twoHoursAgo := big.NewInt(now.Add(-2 * time.Hour).Unix())
	price := big.NewInt(262199000000)

	// Fresh, complete round: the answer passes through unchanged.
	got, err := validateChainlinkRound(chainlinkRound{
		roundID: big.NewInt(100), answer: price, updatedAt: tenMinAgo, answeredInRound: big.NewInt(100),
	}, maxAge, now)
	if err != nil {
		t.Fatalf("fresh round: unexpected error: %v", err)
	}
	if got.Cmp(price) != 0 {
		t.Fatalf("fresh round: answer = %s, want %s", got, price)
	}

	// Each unhealthy round must be rejected; the answer must never leak through.
	bad := []struct {
		name  string
		round chainlinkRound
	}{
		{"stale: updated 2h ago, max 1h", chainlinkRound{big.NewInt(100), price, twoHoursAgo, big.NewInt(100)}},
		{"negative price", chainlinkRound{big.NewInt(100), big.NewInt(-1), tenMinAgo, big.NewInt(100)}},
		{"zero price", chainlinkRound{big.NewInt(100), big.NewInt(0), tenMinAgo, big.NewInt(100)}},
		{"round not complete (updatedAt 0)", chainlinkRound{big.NewInt(100), price, big.NewInt(0), big.NewInt(100)}},
		{"carried-over answer (answeredInRound < roundId)", chainlinkRound{big.NewInt(100), price, tenMinAgo, big.NewInt(99)}},
		{"nil answer", chainlinkRound{big.NewInt(100), nil, tenMinAgo, big.NewInt(100)}},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := validateChainlinkRound(tt.round, maxAge, now); err == nil {
				t.Fatalf("expected error, got answer %s", got)
			}
		})
	}
}

func TestChainlinkMaxAge(t *testing.T) {
	if d := chainlinkMaxAge(nil); d != chainlinkDefaultMaxAge {
		t.Fatalf("no arg: got %s, want default %s", d, chainlinkDefaultMaxAge)
	}
	if d := chainlinkMaxAge([]int{0}); d != chainlinkDefaultMaxAge {
		t.Fatalf("zero arg: got %s, want default %s", d, chainlinkDefaultMaxAge)
	}
	if d := chainlinkMaxAge([]int{3600}); d != time.Hour {
		t.Fatalf("explicit arg: got %s, want 1h", d)
	}
}
