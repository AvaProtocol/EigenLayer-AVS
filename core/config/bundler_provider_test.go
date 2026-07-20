package config

import "testing"

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
