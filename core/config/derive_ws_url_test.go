package config

import "testing"

func TestDeriveWsURL(t *testing.T) {
	cases := []struct {
		in, want, name string
	}{
		{"https://api-ethereum-mainnet.n.dwellir.com/<key>", "wss://api-ethereum-mainnet.n.dwellir.com/<key>", "https → wss (dwellir mainnet)"},
		{"https://sepolia.gateway.tenderly.co/abc", "wss://sepolia.gateway.tenderly.co/abc", "https → wss (tenderly)"},
		{"http://localhost:8545", "ws://localhost:8545", "http → ws (local dev)"},
		{"", "", "empty in → empty out (caller skips WS init)"},
		// Unknown scheme passes through unchanged — the WS client will
		// surface a clear error rather than us silently mangling.
		{"ipc:///tmp/geth.ipc", "ipc:///tmp/geth.ipc", "unknown scheme preserved"},
		{"foo-bar", "foo-bar", "no scheme preserved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DeriveWsURL(c.in)
			if got != c.want {
				t.Fatalf("DeriveWsURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
