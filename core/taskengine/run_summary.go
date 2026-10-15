package taskengine

import (
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

// runSummaryBodyKeys are the Studio report fields a kept summary body must
// carry at least one of. A REST payload that merely has subject and body
// is not a run summary.
var runSummaryBodyKeys = []string{
	"summary",
	"executions",
	"errors",
	"notice",
	"executedSteps",
	"totalSteps",
	"skippedSteps",
}

// studioNotifyStepData keeps Studio's /api/notify reply as {subject, body}.
// body stays the structured report (executions, errors, notice, counts).
// A missing summary object returns false and the caller leaves the response alone.
func studioNotifyStepData(jsonData interface{}) (map[string]interface{}, bool) {
	envelope, ok := jsonData.(map[string]interface{})
	if !ok {
		return nil, false
	}
	summary, ok := envelope["summary"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	subject, _ := summary["subject"].(string)
	body, hasBody := summary["body"]
	if subject == "" && !hasBody {
		return nil, false
	}
	kept := map[string]interface{}{"subject": subject}
	if hasBody {
		kept["body"] = body
	}
	return kept, true
}

// isRunSummary reports whether a REST step's data payload is the kept
// Studio summary ({subject, body} with a structured body). The shape is
// not unique, so callers also require stepIsStudioSummary.
func isRunSummary(payload map[string]interface{}) bool {
	if _, ok := payload["subject"].(string); !ok {
		return false
	}
	body, ok := payload["body"].(map[string]interface{})
	if !ok {
		return false
	}
	for _, key := range runSummaryBodyKeys {
		if _, present := body[key]; present {
			return true
		}
	}
	return false
}

// stepIsStudioSummary reports whether this REST step is the Studio summary
// node. options.summarize opts in, and the request URL has to be
// /api/notify, the same gate that rewrites the reply. The URL checked
// here is the resolved one stored on the step output. The configured URL
// may still be a template.
func stepIsStudioSummary(step *avsproto.Execution_Step, requestURL string) bool {
	if step == nil || detectNotificationProvider(requestURL) != "studio-notify" {
		return false
	}
	configValue := step.GetConfig()
	if configValue == nil {
		return false
	}
	config, ok := configValue.AsInterface().(map[string]interface{})
	if !ok {
		return false
	}
	options, ok := config["options"].(map[string]interface{})
	if !ok {
		return false
	}
	enabled, ok := options["summarize"].(bool)
	return ok && enabled
}

// runSummaryFromSteps returns the last Studio summary step on the run.
// A restApi reply that merely looks like {subject, body} is left alone.
func runSummaryFromSteps(steps []*avsproto.Execution_Step) *structpb.Value {
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step == nil {
			continue
		}
		rest := step.GetRestApi()
		if rest == nil || rest.GetData() == nil {
			continue
		}
		wrapper, ok := rest.GetData().AsInterface().(map[string]interface{})
		if !ok {
			continue
		}
		inner, ok := wrapper["data"].(map[string]interface{})
		if !ok || !isRunSummary(inner) {
			continue
		}
		requestURL, _ := wrapper["url"].(string)
		if !stepIsStudioSummary(step, requestURL) {
			continue
		}
		value, err := structpb.NewValue(inner)
		if err != nil {
			return nil
		}
		return value
	}
	return nil
}

// attachRunSummary copies the summary step onto Execution.summary once the
// run has ended. A run with no summary step, and a summary already set,
// are left unchanged. protojson EmitUnpopulated writes a null summary on
// the waiting checkpoint, and unmarshalling that yields an empty Value.
// That empty value is still unset.
func attachRunSummary(execution *avsproto.Execution) {
	if execution == nil || runSummaryIsSet(execution.GetSummary()) {
		return
	}
	if summary := runSummaryFromSteps(execution.GetSteps()); summary != nil {
		execution.Summary = summary
	}
}

// runSummaryIsSet reports whether summary is the kept {subject, body} object.
// Nil, JSON null, and an empty Value are unset.
func runSummaryIsSet(summary *structpb.Value) bool {
	_, ok := summary.AsInterface().(map[string]interface{})
	return ok
}
