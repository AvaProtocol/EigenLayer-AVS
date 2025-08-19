package preset

import (
	"os"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

func TestPrintSepoliaEnv(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("CI set")
	}
	rpc := testutil.GetTestRPC()
	bundler := testutil.GetTestBundlerRPC()
	t.Logf("SEPOLIA_RPC=%s", rpc)
	t.Logf("SEPOLIA_BUNDLER_RPC=%s", bundler)
	if rpc == "" || bundler == "" {
		t.Fatalf("config not loaded: SEPOLIA_RPC='%s' SEPOLIA_BUNDLER_RPC='%s'", rpc, bundler)
	}
}
