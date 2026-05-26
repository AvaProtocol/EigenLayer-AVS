package rest

import (
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/mapping"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Triggers resource — see api/openapi.yaml `tags: [Triggers]`.

// RunTrigger — POST /api/v1/triggers:run
//
// Evaluate a trigger definition against inline input. SDK testing flows
// use this to confirm a trigger config parses and returns the expected
// shape before committing it to a workflow.
func (s *Server) RunTrigger(ctx echo.Context) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	var body generated.RunTriggerRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("TRIGGERS_BAD_REQUEST", "Invalid request body", err.Error())
	}

	trigger, err := mapping.OpenAPIToProtoTrigger(body.Trigger)
	if err != nil {
		return badRequest("TRIGGERS_BAD_TRIGGER", "Invalid trigger payload", err.Error())
	}

	req := &avsproto.RunTriggerReq{Trigger: trigger}
	if body.TriggerInput != nil {
		converted, err := openAPIInputVarsToProto(*body.TriggerInput)
		if err != nil {
			return badRequest("TRIGGERS_BAD_INPUT", "Invalid triggerInput payload", err.Error())
		}
		req.TriggerInput = converted
	}

	resp, err := s.engine.RunTriggerRPC(user, req)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, runTriggerRespToOpenAPI(resp))
}

// runTriggerRespToOpenAPI mirrors runNodeRespToOpenAPI for the trigger
// side. The per-trigger-type OutputData oneof gets surfaced as the
// `output` map.
func runTriggerRespToOpenAPI(in *avsproto.RunTriggerResp) generated.RunTriggerResponse {
	out := generated.RunTriggerResponse{Success: in.GetSuccess()}
	if msg := in.GetError(); msg != "" {
		out.Error = &msg
	}
	if code := in.GetErrorCode().String(); code != "" && code != "ERROR_CODE_UNSPECIFIED" {
		out.ErrorCode = &code
	}
	if md := in.GetMetadata(); md != nil {
		if v, ok := md.AsInterface().(map[string]interface{}); ok && v != nil {
			out.Metadata = &v
		}
	}
	if raw, err := (protojson.MarshalOptions{}).Marshal(in); err == nil {
		var envelope map[string]interface{}
		if json.Unmarshal(raw, &envelope) == nil {
			for _, key := range []string{"blockTrigger", "fixedTimeTrigger", "cronTrigger", "eventTrigger", "manualTrigger"} {
				if v, ok := envelope[key].(map[string]interface{}); ok {
					out.Output = &v
					break
				}
			}
		}
	}
	return out
}
