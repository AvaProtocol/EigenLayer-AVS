package macros

import (
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestValidateChainlinkRound(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	maxAge := time.Hour
	tenMinAgo := big.NewInt(now.Add(-10 * time.Minute).Unix())
	twoHoursAgo := big.NewInt(now.Add(-2 * time.Hour).Unix())
	// age == maxAge must still pass (staleness uses age > maxAge, not >=).
	exactlyMaxAge := big.NewInt(now.Add(-maxAge).Unix())
	// one second past maxAge must fail.
	oneSecOver := big.NewInt(now.Add(-(maxAge + time.Second)).Unix())
	price := big.NewInt(262199000000)

	// Healthy rounds: the answer passes through unchanged.
	good := []struct {
		name  string
		round chainlinkRound
	}{
		{"fresh equal round ids", chainlinkRound{big.NewInt(100), price, tenMinAgo, big.NewInt(100)}},
		// Chainlink allows answeredInRound >= roundId (answer may land in a later round).
		{"answeredInRound > roundId", chainlinkRound{big.NewInt(100), price, tenMinAgo, big.NewInt(101)}},
		{"age exactly maxAge", chainlinkRound{big.NewInt(100), price, exactlyMaxAge, big.NewInt(100)}},
	}
	for _, tt := range good {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateChainlinkRound(tt.round, maxAge, now)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Cmp(price) != 0 {
				t.Fatalf("answer = %s, want %s", got, price)
			}
		})
	}

	// Each unhealthy round must be rejected; the answer must never leak through.
	bad := []struct {
		name       string
		round      chainlinkRound
		errContain string
	}{
		{"stale: updated 2h ago, max 1h", chainlinkRound{big.NewInt(100), price, twoHoursAgo, big.NewInt(100)}, "stale price"},
		{"stale: one second over maxAge", chainlinkRound{big.NewInt(100), price, oneSecOver, big.NewInt(100)}, "stale price"},
		{"negative price", chainlinkRound{big.NewInt(100), big.NewInt(-1), tenMinAgo, big.NewInt(100)}, "invalid price"},
		{"zero price", chainlinkRound{big.NewInt(100), big.NewInt(0), tenMinAgo, big.NewInt(100)}, "invalid price"},
		{"round not complete (updatedAt 0)", chainlinkRound{big.NewInt(100), price, big.NewInt(0), big.NewInt(100)}, "round not complete"},
		{"carried-over answer (answeredInRound < roundId)", chainlinkRound{big.NewInt(100), price, tenMinAgo, big.NewInt(99)}, "stale round"},
		{"nil answer", chainlinkRound{big.NewInt(100), nil, tenMinAgo, big.NewInt(100)}, "incomplete"},
		{"nil updatedAt", chainlinkRound{big.NewInt(100), price, nil, big.NewInt(100)}, "incomplete"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateChainlinkRound(tt.round, maxAge, now)
			if err == nil {
				t.Fatalf("expected error, got answer %s", got)
			}
			if tt.errContain != "" && !strings.Contains(err.Error(), tt.errContain) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.errContain)
			}
		})
	}
}

func TestChainlinkMaxAge(t *testing.T) {
	if d := chainlinkMaxAge(nil); d != chainlinkDefaultMaxAge {
		t.Fatalf("no arg: got %s, want default %s", d, chainlinkDefaultMaxAge)
	}
	if d := chainlinkMaxAge([]int{}); d != chainlinkDefaultMaxAge {
		t.Fatalf("empty slice: got %s, want default %s", d, chainlinkDefaultMaxAge)
	}
	if d := chainlinkMaxAge([]int{0}); d != chainlinkDefaultMaxAge {
		t.Fatalf("zero arg: got %s, want default %s", d, chainlinkDefaultMaxAge)
	}
	if d := chainlinkMaxAge([]int{-1}); d != chainlinkDefaultMaxAge {
		t.Fatalf("negative arg: got %s, want default %s", d, chainlinkDefaultMaxAge)
	}
	if d := chainlinkMaxAge([]int{3600}); d != time.Hour {
		t.Fatalf("explicit arg: got %s, want 1h", d)
	}
	// Only the first override is used when multiple are supplied.
	if d := chainlinkMaxAge([]int{60, 3600}); d != time.Minute {
		t.Fatalf("multi arg: got %s, want 1m (first wins)", d)
	}
}

func TestBigFromABI(t *testing.T) {
	want := big.NewInt(42)
	if got := bigFromABI(want); got.Cmp(want) != 0 {
		t.Fatalf("big.Int: got %v, want %v", got, want)
	}
	if got := bigFromABI(nil); got != nil {
		t.Fatalf("nil: got %v, want nil", got)
	}
	if got := bigFromABI("not-a-bigint"); got != nil {
		t.Fatalf("wrong type: got %v, want nil", got)
	}
}
