package mapping

import (
	"fmt"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// OpenAPIToProtoTrigger reads the OpenAPI Trigger discriminated union and
// produces an engine-side *avsproto.TaskTrigger. Returns an error if the
// discriminator is missing or names a variant we don't know.
func OpenAPIToProtoTrigger(in generated.Trigger) (*avsproto.TaskTrigger, error) {
	out := &avsproto.TaskTrigger{Name: in.Name}
	if in.Id != nil {
		out.Id = *in.Id
	}

	discriminator, err := in.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("trigger: missing discriminator: %w", err)
	}

	switch discriminator {
	case string(generated.TriggerTypeBlock):
		v, err := in.AsBlockTrigger()
		if err != nil {
			return nil, fmt.Errorf("trigger: decode BlockTrigger: %w", err)
		}
		out.Type = avsproto.TriggerType_TRIGGER_TYPE_BLOCK
		out.TriggerType = &avsproto.TaskTrigger_Block{Block: openAPIBlockToProto(v)}
	case string(generated.TriggerTypeCron):
		v, err := in.AsCronTrigger()
		if err != nil {
			return nil, fmt.Errorf("trigger: decode CronTrigger: %w", err)
		}
		out.Type = avsproto.TriggerType_TRIGGER_TYPE_CRON
		out.TriggerType = &avsproto.TaskTrigger_Cron{Cron: openAPICronToProto(v)}
	case string(generated.TriggerTypeFixedTime):
		v, err := in.AsFixedTimeTrigger()
		if err != nil {
			return nil, fmt.Errorf("trigger: decode FixedTimeTrigger: %w", err)
		}
		out.Type = avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME
		out.TriggerType = &avsproto.TaskTrigger_FixedTime{FixedTime: openAPIFixedTimeToProto(v)}
	case string(generated.TriggerTypeEvent):
		v, err := in.AsEventTrigger()
		if err != nil {
			return nil, fmt.Errorf("trigger: decode EventTrigger: %w", err)
		}
		out.Type = avsproto.TriggerType_TRIGGER_TYPE_EVENT
		out.TriggerType = &avsproto.TaskTrigger_Event{Event: openAPIEventToProto(v)}
	case string(generated.TriggerTypeManual):
		v, err := in.AsManualTrigger()
		if err != nil {
			return nil, fmt.Errorf("trigger: decode ManualTrigger: %w", err)
		}
		out.Type = avsproto.TriggerType_TRIGGER_TYPE_MANUAL
		out.TriggerType = &avsproto.TaskTrigger_Manual{Manual: openAPIManualToProto(v)}
	default:
		return nil, fmt.Errorf("trigger: unknown type %q", discriminator)
	}

	return out, nil
}

// ProtoToOpenAPITrigger lifts an engine TaskTrigger into the REST
// envelope. Variants populated via the From* helpers so the JSON
// serialization handles the discriminator correctly.
func ProtoToOpenAPITrigger(in *avsproto.TaskTrigger) (generated.Trigger, error) {
	out := generated.Trigger{Name: in.GetName()}
	if id := in.GetId(); id != "" {
		out.Id = &id
	}

	switch in.GetType() {
	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		out.Type = generated.TriggerTypeBlock
		if err := out.FromBlockTrigger(protoBlockToOpenAPI(in.GetBlock())); err != nil {
			return out, err
		}
	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		out.Type = generated.TriggerTypeCron
		if err := out.FromCronTrigger(protoCronToOpenAPI(in.GetCron())); err != nil {
			return out, err
		}
	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		out.Type = generated.TriggerTypeFixedTime
		if err := out.FromFixedTimeTrigger(protoFixedTimeToOpenAPI(in.GetFixedTime())); err != nil {
			return out, err
		}
	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		out.Type = generated.TriggerTypeEvent
		if err := out.FromEventTrigger(protoEventToOpenAPI(in.GetEvent())); err != nil {
			return out, err
		}
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		out.Type = generated.TriggerTypeManual
		if err := out.FromManualTrigger(protoManualToOpenAPI(in.GetManual())); err != nil {
			return out, err
		}
	default:
		return out, fmt.Errorf("trigger: unsupported proto type %v", in.GetType())
	}

	return out, nil
}

// ---------------------------------------------------------------------
// BlockTrigger
// ---------------------------------------------------------------------

func openAPIBlockToProto(in generated.BlockTrigger) *avsproto.BlockTrigger {
	out := &avsproto.BlockTrigger{Config: &avsproto.BlockTrigger_Config{}}
	if in.Config != nil {
		out.Config.Interval = in.Config.Interval
		if in.Config.ChainId != nil {
			out.Config.ChainId = *in.Config.ChainId
		}
	}
	return out
}

func protoBlockToOpenAPI(in *avsproto.BlockTrigger) generated.BlockTrigger {
	t := generated.BlockTriggerTypeBlock
	out := generated.BlockTrigger{Type: &t}
	if cfg := in.GetConfig(); cfg != nil {
		c := generated.BlockTriggerConfig{Interval: cfg.GetInterval()}
		if cid := cfg.GetChainId(); cid != 0 {
			c.ChainId = &cid
		}
		out.Config = &c
	}
	return out
}

// ---------------------------------------------------------------------
// CronTrigger
// ---------------------------------------------------------------------

func openAPICronToProto(in generated.CronTrigger) *avsproto.CronTrigger {
	out := &avsproto.CronTrigger{Config: &avsproto.CronTrigger_Config{}}
	if in.Config != nil {
		out.Config.Schedules = in.Config.Schedules
		// CronTrigger_Config currently has no Timezone field in proto;
		// OpenAPI's timezone is dropped on the way in until the proto
		// is extended.
	}
	return out
}

func protoCronToOpenAPI(in *avsproto.CronTrigger) generated.CronTrigger {
	t := generated.Cron
	out := generated.CronTrigger{Type: &t}
	if cfg := in.GetConfig(); cfg != nil {
		out.Config = &generated.CronTriggerConfig{Schedules: cfg.GetSchedules()}
	}
	return out
}

// ---------------------------------------------------------------------
// FixedTimeTrigger
// ---------------------------------------------------------------------

func openAPIFixedTimeToProto(in generated.FixedTimeTrigger) *avsproto.FixedTimeTrigger {
	out := &avsproto.FixedTimeTrigger{Config: &avsproto.FixedTimeTrigger_Config{}}
	if in.Config != nil {
		out.Config.Epochs = in.Config.Epochs
	}
	return out
}

func protoFixedTimeToOpenAPI(in *avsproto.FixedTimeTrigger) generated.FixedTimeTrigger {
	t := generated.FixedTime
	out := generated.FixedTimeTrigger{Type: &t}
	if cfg := in.GetConfig(); cfg != nil {
		out.Config = &generated.FixedTimeTriggerConfig{Epochs: cfg.GetEpochs()}
	}
	return out
}

// ---------------------------------------------------------------------
// EventTrigger — has the deepest nested structure (queries[].topics[])
// and uses helpers in event_query.go to keep this file focused on the
// outer wiring.
// ---------------------------------------------------------------------

func openAPIEventToProto(in generated.EventTrigger) *avsproto.EventTrigger {
	out := &avsproto.EventTrigger{Config: &avsproto.EventTrigger_Config{}}
	if in.Config != nil {
		out.Config.Queries = openAPIEventQueriesToProto(in.Config.Queries)
	}
	return out
}

func protoEventToOpenAPI(in *avsproto.EventTrigger) generated.EventTrigger {
	t := generated.Event
	out := generated.EventTrigger{Type: &t}
	if cfg := in.GetConfig(); cfg != nil {
		out.Config = &generated.EventTriggerConfig{Queries: protoEventQueriesToOpenAPI(cfg.GetQueries())}
	}
	return out
}

// ---------------------------------------------------------------------
// ManualTrigger
// ---------------------------------------------------------------------

func openAPIManualToProto(in generated.ManualTrigger) *avsproto.ManualTrigger {
	out := &avsproto.ManualTrigger{Config: &avsproto.ManualTrigger_Config{}}
	// ManualTrigger_Config is empty in the current proto — webhook bodies
	// flow through inputVariables, not the config object.
	return out
}

func protoManualToOpenAPI(in *avsproto.ManualTrigger) generated.ManualTrigger {
	t := generated.Manual
	return generated.ManualTrigger{Type: &t, Config: &generated.ManualTriggerConfig{}}
}
