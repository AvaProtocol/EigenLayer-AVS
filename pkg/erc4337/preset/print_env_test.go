package preset

import (
	"os"
	"testing"
)

func TestPrintSepoliaEnv(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("CI set")
	}
	rpc := os.Getenv("SEPOLIA_RPC")
	bundler := os.Getenv("SEPOLIA_BUNDLER_RPC")
	t.Logf("SEPOLIA_RPC=%s", rpc)
	t.Logf("SEPOLIA_BUNDLER_RPC=%s", bundler)
	if rpc == "" || bundler == "" {
		t.Fatalf("envs not loaded: SEPOLIA_RPC='%s' SEPOLIA_BUNDLER_RPC='%s'", rpc, bundler)
	}
}
