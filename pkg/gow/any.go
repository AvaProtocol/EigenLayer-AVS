// gow is grpc over the wire
// a package tht help encode native Go type to a compatible data that can be ship over the wire
package gow

import (
	"encoding/json"
	"math/big"
	"reflect"

	"github.com/samber/lo"
	"google.golang.org/protobuf/encoding/protojson"
	anypb "google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

func SliceToStructPbSlice(arr []interface{}) []*structpb.Value {
	data := lo.Map(arr, func(item interface{}, _ int) *structpb.Value {
		value, err := structpb.NewValue(item)

		if err != nil {
			t := reflect.TypeOf(item)
			if t == nil {
				return nil
			}

			// Check both pointer and value types
			if t == reflect.TypeOf(&big.Int{}) || t == reflect.TypeOf(big.Int{}) {
				value, _ := structpb.NewValue(item.(*big.Int).String())
				return value
			}
			return nil
		}

		return value
	})

	return data
}

func SliceToStructPbValue(arr []interface{}) (*structpb.Value, error) {
	list, err := structpb.NewList(arr)
	if err != nil {
		return nil, err
	}
	return structpb.NewValue(list)
}

// Grpc -> Go
func StructPbSliceToSlice(arr []*structpb.Value) []any {
	result := make([]any, 0, len(arr))

	for _, value := range arr {
		// Depending on the kind of value, extract appropriately
		switch {
		case value.GetStringValue() != "":
			result = append(result, value.GetStringValue())
		case value.GetNumberValue() != 0:
			result = append(result, value.GetNumberValue())
		case value.GetBoolValue():
			result = append(result, value.GetBoolValue())
		case value.GetStructValue() != nil:
			result = append(result, value.GetStructValue().AsMap())
		case value.GetListValue() != nil:
			result = append(result, value.GetListValue().AsSlice())
		case value.GetNullValue() == structpb.NullValue_NULL_VALUE:
			result = append(result, nil)
		}
	}

	return result
}

func AnyToString(value *anypb.Any) string {
	wrapper := &structpb.Value{}
	err := value.UnmarshalTo(wrapper)
	if err != nil {
		return ""
	}
	return wrapper.GetStringValue()
}

func AnyToMap(value *anypb.Any) map[string]any {
	wrapper := &structpb.Value{}
	if err := value.UnmarshalTo(wrapper); err != nil {
		return nil
	}

	structValue := wrapper.GetStructValue()
	return structValue.AsMap()
}

func AnyToSlice(value *anypb.Any) []any {
	wrapper := &structpb.Value{}
	if err := value.UnmarshalTo(wrapper); err != nil {
		return nil
	}

	return wrapper.GetListValue().AsSlice()
}

func AnySliceToSlice(anySlice []*anypb.Any) []any {
	result := make([]any, 0, len(anySlice))

	for _, anyVal := range anySlice {
		value := &structpb.Value{}
		if err := anyVal.UnmarshalTo(value); err != nil {
			return nil
		}

		// Depending on the kind of value, extract appropriately
		switch {
		case value.GetStringValue() != "":
			result = append(result, value.GetStringValue())
		case value.GetNumberValue() != 0:
			result = append(result, value.GetNumberValue())
		case value.GetBoolValue():
			result = append(result, value.GetBoolValue())
		case value.GetStructValue() != nil:
			result = append(result, value.GetStructValue().AsMap())
		case value.GetListValue() != nil:
			result = append(result, value.GetListValue().AsSlice())
		case value.GetNullValue() == structpb.NullValue_NULL_VALUE:
			result = append(result, nil)
		}
	}

	return result
}

func AnyToBool(anyVal *anypb.Any) bool {
	value := &structpb.Value{}
	if err := anyVal.UnmarshalTo(value); err != nil {
		return false
	}
	return value.GetBoolValue()
}

func AnyToStruct(anyVal *anypb.Any, target interface{}) error {
	value := &structpb.Value{}
	if err := anyVal.UnmarshalTo(value); err != nil {
		return nil
	}

	marshaler := protojson.MarshalOptions{
		UseProtoNames:   true, // Use proto field names instead of lowerCamelCase
		EmitUnpopulated: true, // Include zero values
	}

	jsonBytes, err := marshaler.Marshal(value)
	if err != nil {
		return err
	}

	// Then unmarshal JSON to your Go struct
	return json.Unmarshal(jsonBytes, target)
}
