package cmd

import "testing"

// StartPeriodicBackup failure is fatal to aggregator.Start. That only
// reaches the process exit if this command uses RunE; cobra Run ignores
// the return value (Claude on #786).
func TestRunAggregatorUsesRunE(t *testing.T) {
	if runAggregatorCmd.RunE == nil {
		t.Fatal("aggregator command must use RunE so Start errors exit 1")
	}
	if runAggregatorCmd.Run != nil {
		t.Fatal("aggregator command must not set Run; it discards errors")
	}
}
