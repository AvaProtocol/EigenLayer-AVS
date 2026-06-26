package mapping

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// TestTriggerRoundTrip walks each trigger variant through
// OpenAPI → proto → OpenAPI and asserts the wire-significant fields
// survive. Catches discriminator typos and missed config fields.
func TestTriggerRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		build func() generated.Trigger
		check func(t *testing.T, out generated.Trigger)
	}{
		{
			name: "block",
			build: func() generated.Trigger {
				tr := generated.Trigger{Name: "blockTrigger", Type: generated.TriggerTypeBlock}
				chainID := int64(11155111)
				typ := generated.BlockTriggerTypeBlock
				inner := generated.BlockTrigger{Type: &typ, Config: &generated.BlockTriggerConfig{Interval: 10, ChainId: &chainID}}
				require.NoError(t, tr.FromBlockTrigger(inner))
				return tr
			},
			check: func(t *testing.T, out generated.Trigger) {
				v, err := out.AsBlockTrigger()
				require.NoError(t, err)
				assert.Equal(t, int64(10), v.Config.Interval)
				require.NotNil(t, v.Config.ChainId)
				assert.Equal(t, int64(11155111), *v.Config.ChainId)
			},
		},
		{
			name: "cron",
			build: func() generated.Trigger {
				tr := generated.Trigger{Name: "cronTrigger", Type: generated.TriggerTypeCron}
				typ := generated.Cron
				inner := generated.CronTrigger{Type: &typ, Config: &generated.CronTriggerConfig{Schedules: []string{"*/5 * * * *", "0 0 * * *"}}}
				require.NoError(t, tr.FromCronTrigger(inner))
				return tr
			},
			check: func(t *testing.T, out generated.Trigger) {
				v, err := out.AsCronTrigger()
				require.NoError(t, err)
				assert.Equal(t, []string{"*/5 * * * *", "0 0 * * *"}, v.Config.Schedules)
			},
		},
		{
			name: "fixedTime",
			build: func() generated.Trigger {
				tr := generated.Trigger{Name: "fixedTrigger", Type: generated.TriggerTypeFixedTime}
				typ := generated.FixedTime
				inner := generated.FixedTimeTrigger{Type: &typ, Config: &generated.FixedTimeTriggerConfig{Epochs: []int64{1748000000000, 1748100000000}}}
				require.NoError(t, tr.FromFixedTimeTrigger(inner))
				return tr
			},
			check: func(t *testing.T, out generated.Trigger) {
				v, err := out.AsFixedTimeTrigger()
				require.NoError(t, err)
				assert.Equal(t, []int64{1748000000000, 1748100000000}, v.Config.Epochs)
			},
		},
		{
			name: "manual",
			build: func() generated.Trigger {
				tr := generated.Trigger{Name: "manualTrigger", Type: generated.TriggerTypeManual}
				typ := generated.Manual
				inner := generated.ManualTrigger{Type: &typ, Config: &generated.ManualTriggerConfig{Lang: generated.Lang("json")}}
				require.NoError(t, tr.FromManualTrigger(inner))
				return tr
			},
			check: func(t *testing.T, out generated.Trigger) {
				v, err := out.AsManualTrigger()
				require.NoError(t, err)
				assert.NotNil(t, v.Config)
			},
		},
		{
			name: "event",
			build: func() generated.Trigger {
				tr := generated.Trigger{Name: "eventTrigger", Type: generated.TriggerTypeEvent}
				addr := generated.EthereumAddress("0x1234567890123456789012345678901234567890")
				sig := "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
				eventChainID := int64(8453)
				typ := generated.Event
				inner := generated.EventTrigger{
					Type: &typ,
					Config: &generated.EventTriggerConfig{
						ChainId: &eventChainID,
						Queries: []generated.EventTriggerQuery{{
							Addresses: &[]generated.EthereumAddress{addr},
							Topics:    &[]string{sig, ""},
							Conditions: &[]generated.EventCondition{{
								FieldName: "AnswerUpdated.current",
								Operator:  generated.Gt,
								Value:     "200000000000",
								FieldType: func() *string { s := "int256"; return &s }(),
							}},
						}},
					},
				}
				require.NoError(t, tr.FromEventTrigger(inner))
				return tr
			},
			check: func(t *testing.T, out generated.Trigger) {
				v, err := out.AsEventTrigger()
				require.NoError(t, err)
				require.NotNil(t, v.Config.ChainId, "event trigger chain_id must survive the round-trip (G1)")
				assert.Equal(t, int64(8453), *v.Config.ChainId)
				require.Len(t, v.Config.Queries, 1)
				require.NotNil(t, v.Config.Queries[0].Addresses)
				assert.Equal(t, "0x1234567890123456789012345678901234567890", string((*v.Config.Queries[0].Addresses)[0]))
				require.NotNil(t, v.Config.Queries[0].Topics)
				topics := *v.Config.Queries[0].Topics
				require.Len(t, topics, 2)
				assert.Equal(t, "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", topics[0])
				assert.Equal(t, "", topics[1], "wildcard topic round-trips as empty string in the []string shape")
				require.NotNil(t, v.Config.Queries[0].Conditions)
				conds := *v.Config.Queries[0].Conditions
				require.Len(t, conds, 1)
				assert.Equal(t, "AnswerUpdated.current", conds[0].FieldName)
				assert.Equal(t, generated.Gt, conds[0].Operator)
				assert.Equal(t, "200000000000", conds[0].Value, "condition value is a plain string, matching the proto")
				require.NotNil(t, conds[0].FieldType)
				assert.Equal(t, "int256", *conds[0].FieldType)
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			start := tc.build()
			proto, err := OpenAPIToProtoTrigger(start)
			require.NoError(t, err)
			roundTripped, err := ProtoToOpenAPITrigger(proto)
			require.NoError(t, err)
			tc.check(t, roundTripped)
		})
	}
}
