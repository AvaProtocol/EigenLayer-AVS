package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadYamlConfigTrimsEnvVarWhitespace pins the production bug we hit
// with Railway env vars: dashboard paste accidentally added a trailing
// newline to BNB_RPC, which turned "https://rpc/key" into "https://rpc/key\n".
// All subsequent DNS lookups failed opaquely. ReadYamlConfig must trim
// whitespace from env-var expansions so this can't recur.
func TestReadYamlConfigTrimsEnvVarWhitespace(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "test.yaml")
	if err := os.WriteFile(cfgPath, []byte("url: ${TEST_RPC_URL}\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"trailing newline", "https://rpc.example.com/key\n", "https://rpc.example.com/key"},
		{"leading newline", "\nhttps://rpc.example.com/key", "https://rpc.example.com/key"},
		{"surrounding spaces", "  https://rpc.example.com/key  ", "https://rpc.example.com/key"},
		{"tabs and CR", "\thttps://rpc.example.com/key\r\n", "https://rpc.example.com/key"},
		{"already clean", "https://rpc.example.com/key", "https://rpc.example.com/key"},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEST_RPC_URL", tc.raw)
			var parsed struct {
				URL string `yaml:"url"`
			}
			if err := ReadYamlConfig(cfgPath, &parsed); err != nil {
				t.Fatalf("ReadYamlConfig: %v", err)
			}
			if parsed.URL != tc.want {
				t.Fatalf("got %q, want %q", parsed.URL, tc.want)
			}
		})
	}
}
