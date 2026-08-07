package config

import (
	"strings"
	"testing"
)

func TestActiveBundlerURL(t *testing.T) {
	cases := []struct {
		name    string
		cfg     SmartWalletConfig
		want    string
		wantErr bool
	}{
		{
			name: "empty provider defaults to alchemy and derives URL",
			cfg:  SmartWalletConfig{ChainID: 11155111, AlchemyAPIKey: "KEY"},
			want: "https://eth-sepolia.g.alchemy.com/v2/KEY",
		},
		{
			name: "explicit alchemy on base mainnet",
			cfg:  SmartWalletConfig{BundlerProvider: "alchemy", ChainID: 8453, AlchemyAPIKey: "K2"},
			want: "https://base-mainnet.g.alchemy.com/v2/K2",
		},
		{
			name: "explicit alchemy on bnb mainnet",
			cfg:  SmartWalletConfig{BundlerProvider: "alchemy", ChainID: 56, AlchemyAPIKey: "K3"},
			want: "https://bnb-mainnet.g.alchemy.com/v2/K3",
		},
		{
			name: "alchemy path never falls back to bundler_url",
			cfg:  SmartWalletConfig{ChainID: 11155111, AlchemyAPIKey: "K", BundlerURL: "https://voltaire.example/rpc"},
			want: "https://eth-sepolia.g.alchemy.com/v2/K",
		},
		{
			name:    "alchemy without api key is a hard error",
			cfg:     SmartWalletConfig{ChainID: 1},
			wantErr: true,
		},
		{
			name:    "alchemy on an unmapped chain is a hard error",
			cfg:     SmartWalletConfig{ChainID: 999999, AlchemyAPIKey: "K"},
			wantErr: true,
		},
		{
			name: "self_hosted uses bundler_url",
			cfg:  SmartWalletConfig{BundlerProvider: "self_hosted", BundlerURL: "https://voltaire.example/rpc"},
			want: "https://voltaire.example/rpc",
		},
		{
			name:    "self_hosted without bundler_url is a hard error",
			cfg:     SmartWalletConfig{BundlerProvider: "self_hosted"},
			wantErr: true,
		},
		{
			name:    "unknown provider is a hard error",
			cfg:     SmartWalletConfig{BundlerProvider: "pimlico", ChainID: 1, AlchemyAPIKey: "K"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.cfg.ActiveBundlerURL()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got url %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBundlerConfigured(t *testing.T) {
	if !(&SmartWalletConfig{ChainID: 11155111, AlchemyAPIKey: "K"}).BundlerConfigured() {
		t.Fatal("alchemy-configured chain should report configured")
	}
	if (&SmartWalletConfig{ChainID: 11155111}).BundlerConfigured() {
		t.Fatal("alchemy default without key should report not configured (connectivity-only)")
	}
	if (&SmartWalletConfig{BundlerProvider: "self_hosted"}).BundlerConfigured() {
		t.Fatal("self_hosted without url should report not configured")
	}
}

func TestBundlerEndpointLabel(t *testing.T) {
	// Alchemy embeds the key in the path — must never appear in the label.
	alchemy := &SmartWalletConfig{ChainID: 11155111, AlchemyAPIKey: "super-secret-key"}
	got := alchemy.BundlerEndpointLabel()
	if got != "https://eth-sepolia.g.alchemy.com/v2/***" {
		t.Fatalf("alchemy label: got %q", got)
	}
	if strings.Contains(got, "super-secret-key") {
		t.Fatalf("alchemy label leaked key: %q", got)
	}

	// Self-hosted with apikey query param.
	selfHosted := &SmartWalletConfig{
		BundlerProvider: BundlerProviderSelfHosted,
		BundlerURL:      "https://bundler.example/rpc?apikey=sekrit",
	}
	got = selfHosted.BundlerEndpointLabel()
	if got != "https://bundler.example/rpc?***" {
		t.Fatalf("self_hosted label: got %q", got)
	}

	// Unresolved alchemy (no key) — still log-safe.
	if label := (&SmartWalletConfig{ChainID: 1}).BundlerEndpointLabel(); label != "alchemy:unresolved" {
		t.Fatalf("unresolved label: got %q", label)
	}
	if (*SmartWalletConfig)(nil).BundlerEndpointLabel() != "" {
		t.Fatal("nil config should return empty label")
	}
}

// The four chains the expansion targets must resolve to an Alchemy subdomain.
// A missing entry is a hard error at send time (ActiveBundlerURL refuses to
// guess), so it would surface as a chain that boots fine and then cannot
// execute anything.
func TestExpansionChainsHaveAlchemySubdomains(t *testing.T) {
	for _, tc := range []struct {
		chainID int64
		want    string
	}{
		{56, "https://bnb-mainnet.g.alchemy.com/v2/K"},
		{42161, "https://arb-mainnet.g.alchemy.com/v2/K"},
		{999, "https://hyperliquid-mainnet.g.alchemy.com/v2/K"},
		{4663, "https://robinhood-mainnet.g.alchemy.com/v2/K"},
	} {
		c := SmartWalletConfig{ChainID: tc.chainID, BundlerProvider: "alchemy", AlchemyAPIKey: "K"}
		got, err := c.ActiveBundlerURL()
		if err != nil {
			t.Errorf("chain %d: %v", tc.chainID, err)
			continue
		}
		if got != tc.want {
			t.Errorf("chain %d: got %s, want %s", tc.chainID, got, tc.want)
		}
	}
}
