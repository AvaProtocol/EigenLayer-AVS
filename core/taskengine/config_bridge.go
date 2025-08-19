package taskengine

import (
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

// globalAggregatorConfig holds a pointer to the aggregator's loaded configuration
var globalAggregatorConfig *config.Config

// SetGlobalAggregatorConfig sets the global aggregator configuration for access by helpers
func SetGlobalAggregatorConfig(c *config.Config) {
	globalAggregatorConfig = c
}

// GetGlobalAggregatorConfig returns the global aggregator configuration if set
func GetGlobalAggregatorConfig() *config.Config {
	return globalAggregatorConfig
}
