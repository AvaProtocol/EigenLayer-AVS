package services

import (
	"strings"
	"testing"
)

func TestChainIDToMoralisChain(t *testing.T) {
	ms := &MoralisService{}
	cases := []struct {
		chainID int64
		want    string
	}{
		{1, "eth"},
		{11155111, "sepolia"},
		{8453, "base"},
		{84532, "base-sepolia"},
		{56, "bsc"},
		{42161, "arbitrum"},
		{10, "optimism"},
		{137, "polygon"},
		{130, ""},
		{4663, ""},
		{999, ""},
	}
	for _, tc := range cases {
		if got := ms.chainIDToMoralisChain(tc.chainID); got != tc.want {
			t.Errorf("chainIDToMoralisChain(%d) = %q, want %q", tc.chainID, got, tc.want)
		}
	}
}

func TestGetChainTokenMappingWaveA(t *testing.T) {
	m := getChainTokenMapping()

	bnb, ok := m[56]
	if !ok {
		t.Fatal("missing chain 56")
	}
	if bnb.Symbol != "BNB" || bnb.Decimals != 18 {
		t.Errorf("BNB token = %+v", bnb)
	}
	if !strings.EqualFold(bnb.ContractAddr, "0xbb4CdB9CBd36B01bD1cBaEBF2De08d9173bc095c") {
		t.Errorf("WBNB address = %s", bnb.ContractAddr)
	}

	arb, ok := m[42161]
	if !ok {
		t.Fatal("missing chain 42161")
	}
	if arb.Symbol != "ETH" || arb.Decimals != 18 {
		t.Errorf("Arb native = %+v", arb)
	}
	if !strings.EqualFold(arb.ContractAddr, "0x82aF49447D8a07e3bd95BD0d56f35241523fBab1") {
		t.Errorf("Arb WETH address = %s", arb.ContractAddr)
	}

	if !nativePricingSupportedChains[56] || !nativePricingSupportedChains[42161] {
		t.Error("Wave A mainnets must be in nativePricingSupportedChains")
	}

	op, ok := m[10]
	if !ok {
		t.Fatal("missing chain 10")
	}
	if op.Symbol != "ETH" || op.Decimals != 18 {
		t.Errorf("OP native = %+v", op)
	}
	if !strings.EqualFold(op.ContractAddr, "0x4200000000000000000000000000000000000006") {
		t.Errorf("OP WETH address = %s", op.ContractAddr)
	}
	if !nativePricingSupportedChains[10] {
		t.Error("OP Mainnet must be in nativePricingSupportedChains")
	}
	if _, ok := m[130]; ok {
		t.Error("Unichain must not be in chainTokens — Moralis Data API does not list 130; leave it on getETHPrice()")
	}
	if nativePricingSupportedChains[130] {
		t.Error("Unichain must not be in nativePricingSupportedChains")
	}
	if _, ok := m[4663]; ok {
		t.Error("Robinhood must not be in chainTokens — Moralis Data API does not list 4663; leave it on getETHPrice()")
	}
	if nativePricingSupportedChains[4663] {
		t.Error("Robinhood must not be in nativePricingSupportedChains")
	}

	pol, ok := m[137]
	if !ok {
		t.Fatal("missing chain 137")
	}
	if pol.Symbol != "POL" || pol.Decimals != 18 {
		t.Errorf("POL token = %+v", pol)
	}
	if !strings.EqualFold(pol.ContractAddr, "0x0d500B1d8E8eF31E21C99d1Db9A6444d3ADf1270") {
		t.Errorf("WPOL address = %s", pol.ContractAddr)
	}
	if !nativePricingSupportedChains[137] {
		t.Error("Polygon must be in nativePricingSupportedChains")
	}

	hype, ok := m[999]
	if !ok {
		t.Fatal("missing chain 999 — HYPE must be in chainTokens so GetNativeTokenPriceUSD does not ETH-fallback")
	}
	if hype.Symbol != "HYPE" {
		t.Errorf("HYPE token = %+v", hype)
	}
	if nativePricingSupportedChains[999] {
		t.Error("Hyperliquid must not be in nativePricingSupportedChains until Moralis Data API lists 999")
	}

	ms := &MoralisService{chainTokens: m}
	if got := ms.GetNativeTokenSymbol(137); got != "POL" {
		t.Errorf("GetNativeTokenSymbol(137) = %q, want POL", got)
	}
	if got := ms.GetNativeTokenSymbol(999); got != "HYPE" {
		t.Errorf("GetNativeTokenSymbol(999) = %q, want HYPE", got)
	}
}

func TestGetFallbackPriceRefusesNonETH(t *testing.T) {
	ms := &MoralisService{}
	got, err := ms.getFallbackPrice("ETH")
	if err != nil || got == nil {
		t.Fatalf("ETH fallback: %v %v", got, err)
	}
	if f, _ := got.Float64(); f != 2500 {
		t.Errorf("ETH fallback = %v, want 2500", f)
	}

	if _, err := ms.getFallbackPrice("BNB"); err == nil {
		t.Fatal("BNB must not inherit the $2500 ETH fallback")
	}
	if _, err := ms.getFallbackPrice("bnb"); err == nil {
		t.Fatal("bnb (lowercase) must not inherit the ETH fallback")
	}
	if _, err := ms.getFallbackPrice("POL"); err == nil {
		t.Fatal("POL must not inherit the $2500 ETH fallback")
	}
	if _, err := ms.getFallbackPrice("HYPE"); err == nil {
		t.Fatal("HYPE must not inherit the $2500 ETH fallback")
	}
}
