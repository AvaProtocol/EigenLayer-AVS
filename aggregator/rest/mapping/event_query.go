package mapping

import (
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// openAPIEventQueriesToProto maps the nested EventTrigger.queries slice.
// Separated from trigger.go because the per-query mapping is non-trivial
// (topics carry wildcards, conditions carry polymorphic values).
func openAPIEventQueriesToProto(in []generated.EventTriggerQuery) []*avsproto.EventTrigger_Query {
	if len(in) == 0 {
		return nil
	}
	out := make([]*avsproto.EventTrigger_Query, 0, len(in))
	for _, q := range in {
		pq := &avsproto.EventTrigger_Query{}

		if q.Addresses != nil {
			pq.Addresses = make([]string, 0, len(*q.Addresses))
			for _, a := range *q.Addresses {
				pq.Addresses = append(pq.Addresses, string(a))
			}
		}

		if q.Topics != nil {
			// Empty string represents a wildcard at that topic index —
			// matches the operator's filter convention. The OpenAPI
			// generator currently models Topics as []string (not
			// []*string), so JSON nulls round-trip as "".
			pq.Topics = append(pq.Topics, *q.Topics...)
		}

		if q.MaxEventsPerBlock != nil {
			v := uint32(*q.MaxEventsPerBlock)
			pq.MaxEventsPerBlock = &v
		}

		if q.ContractAbi != nil {
			for _, item := range *q.ContractAbi {
				if pv, err := structpb.NewValue(item); err == nil {
					pq.ContractAbi = append(pq.ContractAbi, pv)
				}
			}
		}

		if q.Conditions != nil {
			for _, c := range *q.Conditions {
				pc := &avsproto.EventCondition{
					FieldName: c.FieldName,
					Operator:  string(c.Operator),
				}
				if c.FieldType != nil {
					pc.FieldType = *c.FieldType
				}
				// Value is a plain string on the wire, matching the proto
				// EventCondition.value. The operator parses it according
				// to FieldType (e.g. "200000000000" → big.Int for a
				// uint256/int256 field).
				pc.Value = c.Value
				pq.Conditions = append(pq.Conditions, pc)
			}
		}

		if q.MethodCalls != nil {
			for _, m := range *q.MethodCalls {
				pm := &avsproto.EventTrigger_MethodCall{
					MethodName: m.MethodName,
				}
				if m.ApplyToFields != nil {
					pm.ApplyToFields = *m.ApplyToFields
				}
				if m.MethodParams != nil {
					pm.MethodParams = *m.MethodParams
				}
				if m.CallData != nil {
					cd := string(*m.CallData)
					pm.CallData = &cd
				}
				pq.MethodCalls = append(pq.MethodCalls, pm)
			}
		}

		out = append(out, pq)
	}
	return out
}

// protoEventQueriesToOpenAPI inverts openAPIEventQueriesToProto.
func protoEventQueriesToOpenAPI(in []*avsproto.EventTrigger_Query) []generated.EventTriggerQuery {
	if len(in) == 0 {
		return nil
	}
	out := make([]generated.EventTriggerQuery, 0, len(in))
	for _, q := range in {
		oq := generated.EventTriggerQuery{}

		if len(q.GetAddresses()) > 0 {
			addrs := make([]generated.EthereumAddress, 0, len(q.GetAddresses()))
			for _, a := range q.GetAddresses() {
				addrs = append(addrs, generated.EthereumAddress(a))
			}
			oq.Addresses = &addrs
		}

		if len(q.GetTopics()) > 0 {
			// Wildcards round-trip as the empty string, not JSON null —
			// the OpenAPI generator models Topics as []string.
			topics := append([]string(nil), q.GetTopics()...)
			oq.Topics = &topics
		}

		if mev := q.GetMaxEventsPerBlock(); mev != 0 {
			v := int32(mev)
			oq.MaxEventsPerBlock = &v
		}

		if len(q.GetContractAbi()) > 0 {
			abi := make([]map[string]interface{}, 0, len(q.GetContractAbi()))
			for _, v := range q.GetContractAbi() {
				if obj, ok := v.AsInterface().(map[string]interface{}); ok {
					abi = append(abi, obj)
				}
			}
			oq.ContractAbi = &abi
		}

		if len(q.GetConditions()) > 0 {
			conds := make([]generated.EventCondition, 0, len(q.GetConditions()))
			for _, c := range q.GetConditions() {
				ec := generated.EventCondition{
					FieldName: c.GetFieldName(),
					Operator:  generated.EventConditionOperator(c.GetOperator()),
				}
				if ft := c.GetFieldType(); ft != "" {
					ec.FieldType = &ft
				}
				ec.Value = c.GetValue()
				conds = append(conds, ec)
			}
			oq.Conditions = &conds
		}

		if len(q.GetMethodCalls()) > 0 {
			calls := make([]generated.EventMethodCall, 0, len(q.GetMethodCalls()))
			for _, m := range q.GetMethodCalls() {
				ec := generated.EventMethodCall{
					MethodName: m.GetMethodName(),
				}
				if cd := m.GetCallData(); cd != "" {
					h := generated.Hex(cd)
					ec.CallData = &h
				}
				if af := m.GetApplyToFields(); len(af) > 0 {
					ec.ApplyToFields = &af
				}
				if mp := m.GetMethodParams(); len(mp) > 0 {
					ec.MethodParams = &mp
				}
				calls = append(calls, ec)
			}
			oq.MethodCalls = &calls
		}

		out = append(out, oq)
	}
	return out
}
