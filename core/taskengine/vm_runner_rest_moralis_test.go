package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

const testPlatformMoralisKey = "platform-moralis-should-not-leak"

func newRestVM(t *testing.T, secrets map[string]string) *VM {
	t.Helper()
	vm, err := NewVMWithData(&model.Workflow{
		Task: &avsproto.Task{
			Id: "moralis-test",
			Trigger: &avsproto.TaskTrigger{
				Id:   "triggertest",
				Name: "triggertest",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), secrets)
	require.NoError(t, err)
	return vm
}

// TestRestRequestMoralisWalletBalances documents that restApi must NOT be
// able to send the platform Moralis key via a template. Wallet balances go
// through BalanceNode (macros.secrets, engine-side). A restApi template
// that interpolates {{apContext.configVars.moralis_api_key}} used to turn
// any workflow into a billed Moralis client; that key is now omitted from
// configVars.
func TestRestRequestMoralisWalletBalances(t *testing.T) {
	secrets := map[string]string{
		platformSecretMoralisAPIKey: testPlatformMoralisKey,
		"sendgrid_key":              "user-visible-sendgrid",
	}
	vm := newRestVM(t, secrets)

	expanded := vm.preprocessText("{{apContext.configVars.moralis_api_key}}")
	require.NotEqual(t, testPlatformMoralisKey, expanded, "platform moralis_api_key must not interpolate into restApi templates")
	require.NotContains(t, expanded, testPlatformMoralisKey, "platform moralis_api_key must not leak into restApi headers")

	sendgrid := vm.preprocessText("{{apContext.configVars.sendgrid_key}}")
	require.Equal(t, "user-visible-sendgrid", sendgrid, "notify secrets must still interpolate")
}

func TestRestMoralisAuthProviderInjectsKeyOnMoralisHost(t *testing.T) {
	prev := macroSecrets
	t.Cleanup(func() { SetMacroSecrets(prev) })
	SetMacroSecrets(map[string]string{
		platformSecretMoralisAPIKey: testPlatformMoralisKey,
	})

	moralisURL := "https://deep-index.moralis.io/api/v2.2/wallets/0xabc/approvals?chain=eth"
	require.Equal(t, testPlatformMoralisKey, moralisAPIKeyHeader(moralisURL))

	opts, err := structpb.NewValue(map[string]interface{}{
		"auth": map[string]interface{}{"provider": restAuthProviderMoralis},
	})
	require.NoError(t, err)
	require.Equal(t, restAuthProviderMoralis, restAuthProvider(&avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{Url: moralisURL, Method: "GET", Options: opts},
	}))
}

func TestRestMoralisAuthProviderDoesNotInjectOnForeignHost(t *testing.T) {
	prev := macroSecrets
	t.Cleanup(func() { SetMacroSecrets(prev) })
	SetMacroSecrets(map[string]string{
		platformSecretMoralisAPIKey: testPlatformMoralisKey,
	})

	foreign := []string{
		"https://webhook.site/abc",
		"http://deep-index.moralis.io/api/v2.2/wallets/0xabc/tokens",
		"https://deep-index.moralis.io.evil.com/api/v2.2/wallets/0xabc/tokens",
		"https://evil.com/?host=deep-index.moralis.io",
		"https://user:pass@evil.com/",
		"https://deep-index.moralis.io@evil.com/steal",
	}
	for _, raw := range foreign {
		require.False(t, isMoralisDataAPIURL(raw), raw)
		require.Empty(t, moralisAPIKeyHeader(raw), raw)
	}

	require.True(t, isMoralisDataAPIURL("https://deep-index.moralis.io/api/v2.2/wallets/0x/tokens"))
	require.True(t, isMoralisDataAPIURL("https://deep-index.moralis.io:443/api/v2.2/wallets/0x/tokens"))
}
