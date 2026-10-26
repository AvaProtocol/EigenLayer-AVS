package bundler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewBundlerClient(t *testing.T) {
	t.Run("Valid URL", func(t *testing.T) {
		client, err := NewBundlerClient("http://localhost:8545")
		require.NoError(t, err)
		require.NotNil(t, client)
		defer client.Close()
	})

	t.Run("Invalid URL", func(t *testing.T) {
		// rpc.DialHTTP should fail with a completely malformed URL
		_, err := NewBundlerClient("://invalid-url")
		require.Error(t, err)
	})
}
