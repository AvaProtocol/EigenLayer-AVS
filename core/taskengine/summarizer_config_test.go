package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

// TestNewContextMemorySummarizerFromAggregatorConfig documents the construction contract:
//   - disabled (or nil config)         → (nil, nil)   — deterministic fallback is used
//   - enabled but misconfigured        → (nil, error) — daemon fails fast at startup
//   - enabled and fully configured     → (summarizer, nil)
//
// These error paths have no integration-environment dependency, so they are covered here as
// a plain unit test (runs in the default `core/taskengine` shard, no `integration` tag).
func TestNewContextMemorySummarizerFromAggregatorConfig(t *testing.T) {
	cfg := func(enabled bool, provider, endpoint, key string) *config.Config {
		return &config.Config{
			NotificationsSummary: config.NotificationsSummaryConfig{
				Enabled:     enabled,
				Provider:    provider,
				APIEndpoint: endpoint,
				APIKey:      key,
			},
		}
	}

	const origin = "https://app.avaprotocol.org"

	tests := []struct {
		name      string
		cfg       *config.Config
		wantNil   bool   // summarizer expected to be nil
		wantErr   bool   // error expected
		errSubstr string // substring the error must contain (when wantErr)
	}{
		{name: "nil config", cfg: nil, wantNil: true},
		{name: "disabled", cfg: cfg(false, "context-memory", origin, "k"), wantNil: true},
		{name: "unsupported provider", cfg: cfg(true, "openai", origin, "k"), wantNil: true, wantErr: true, errSubstr: "provider"},
		{name: "empty provider", cfg: cfg(true, "", origin, "k"), wantNil: true, wantErr: true, errSubstr: "provider"},
		{name: "empty endpoint", cfg: cfg(true, "context-memory", "", "k"), wantNil: true, wantErr: true, errSubstr: "api_endpoint"},
		{name: "whitespace endpoint", cfg: cfg(true, "context-memory", "   ", "k"), wantNil: true, wantErr: true, errSubstr: "api_endpoint"},
		{name: "empty key", cfg: cfg(true, "context-memory", origin, ""), wantNil: true, wantErr: true, errSubstr: "api_key"},
		{name: "whitespace key", cfg: cfg(true, "context-memory", origin, "   "), wantNil: true, wantErr: true, errSubstr: "api_key"},
		{name: "fully configured", cfg: cfg(true, "context-memory", origin, "k"), wantNil: false},
		{name: "provider case-insensitive", cfg: cfg(true, "Context-Memory", origin, "k"), wantNil: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewContextMemorySummarizerFromAggregatorConfig(tt.cfg)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil")
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error %q does not mention %q", err.Error(), tt.errSubstr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil && s != nil {
				t.Errorf("expected nil summarizer, got %T", s)
			}
			if !tt.wantNil && s == nil {
				t.Errorf("expected a non-nil summarizer, got nil")
			}
		})
	}
}
